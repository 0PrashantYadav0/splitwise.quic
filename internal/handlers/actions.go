package handlers

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"splitwise-quic/internal/models"
	"splitwise-quic/internal/realtime"
	"splitwise-quic/internal/splits"
)

func (h *Handlers) createGroup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	u := userFrom(r)
	g, err := h.store.CreateGroup(name, u.ID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.store.LogActivity(g.ID, u.ID, "created the group")
	http.Redirect(w, r, "/g/"+g.ID, http.StatusSeeOther)
}

func (h *Handlers) addMember(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	uid := r.FormValue("user_id")
	if uid != "" {
		if err := h.store.AddMember(g.ID, uid); err != nil {
			httpError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if added, err := h.store.UserByID(uid); err == nil {
			_ = h.store.LogActivity(g.ID, userFrom(r).ID, "added "+added.Name)
		}
		h.publish(g.ID, "member", "A member was added")
	}
	http.Redirect(w, r, "/g/"+g.ID, http.StatusSeeOther)
}

func (h *Handlers) createExpense(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	if err := r.ParseForm(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}

	amount, err := parseMoney(r.FormValue("amount"))
	if err != nil || amount <= 0 {
		httpError(w, "invalid amount", http.StatusBadRequest)
		return
	}
	st := models.SplitType(r.FormValue("split_type"))
	included := r.Form["include"]
	if len(included) == 0 {
		httpError(w, "select at least one participant", http.StatusBadRequest)
		return
	}

	inputs, err := h.buildInputs(r, st, included)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}

	e := &models.Expense{
		GroupID:     g.ID,
		PaidBy:      r.FormValue("paid_by"),
		Description: strings.TrimSpace(r.FormValue("description")),
		Amount:      amount,
		Currency:    strings.ToUpper(strings.TrimSpace(r.FormValue("currency"))),
		SplitType:   st,
	}
	if e.Currency == "" {
		e.Currency = "USD"
	}
	if _, err := h.store.CreateExpense(e, inputs); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if payer, err := h.store.UserByID(e.PaidBy); err == nil {
		_ = h.store.LogActivity(g.ID, userFrom(r).ID,
			fmt.Sprintf("added \"%s\" (%s %s paid by %s)", e.Description, e.Currency, render2(e.Amount), payer.Name))
	}
	h.publish(g.ID, "expense", "New expense: "+e.Description)
	http.Redirect(w, r, "/g/"+g.ID, http.StatusSeeOther)
}

// buildInputs converts form values into split inputs for the chosen type.
func (h *Handlers) buildInputs(r *http.Request, st models.SplitType, included []string) ([]splits.Input, error) {
	inputs := make([]splits.Input, 0, len(included))
	for _, uid := range included {
		raw := r.FormValue("value_" + uid)
		var val int64
		switch st {
		case models.SplitEqual:
			val = 0
		case models.SplitExact:
			v, err := parseMoney(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid exact amount")
			}
			val = v
		case models.SplitPercentage:
			f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				return nil, fmt.Errorf("invalid percentage")
			}
			val = int64(math.Round(f * 100)) // percent -> basis points
		case models.SplitShares:
			f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				return nil, fmt.Errorf("invalid share")
			}
			val = int64(math.Round(f))
		default:
			return nil, fmt.Errorf("unknown split type")
		}
		inputs = append(inputs, splits.Input{UserID: uid, Value: val})
	}
	return inputs, nil
}

func (h *Handlers) settle(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	amount, err := parseMoney(r.FormValue("amount"))
	if err != nil || amount <= 0 {
		httpError(w, "invalid amount", http.StatusBadRequest)
		return
	}
	st := &models.Settlement{
		GroupID:  g.ID,
		FromUser: r.FormValue("from_user"),
		ToUser:   r.FormValue("to_user"),
		Amount:   amount,
		Currency: strings.ToUpper(strings.TrimSpace(r.FormValue("currency"))),
	}
	if _, err := h.store.CreateSettlement(st); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	from, _ := h.store.UserByID(st.FromUser)
	to, _ := h.store.UserByID(st.ToUser)
	if from != nil && to != nil {
		_ = h.store.LogActivity(g.ID, userFrom(r).ID,
			fmt.Sprintf("recorded %s paying %s %s %s", from.Name, to.Name, st.Currency, render2(st.Amount)))
	}
	h.publish(g.ID, "settlement", "A payment was settled")
	http.Redirect(w, r, "/g/"+g.ID, http.StatusSeeOther)
}

func (h *Handlers) deleteExpense(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	if err := h.store.DeleteExpense(g.ID, r.PathValue("eid")); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.store.LogActivity(g.ID, userFrom(r).ID, "deleted an expense")
	h.publish(g.ID, "expense", "An expense was deleted")
	http.Redirect(w, r, "/g/"+g.ID, http.StatusSeeOther)
}

func (h *Handlers) publish(groupID, kind, msg string) {
	h.hub.Publish(realtime.Event{GroupID: groupID, Kind: kind, Message: msg})
}

// parseMoney converts a decimal string like "12.34" into minor units (1234).
func parseMoney(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(f * 100)), nil
}

// render2 formats minor units as a 2-decimal string (mirrors render.Money).
func render2(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	out := fmt.Sprintf("%d.%02d", minor/100, minor%100)
	if neg {
		return "-" + out
	}
	return out
}
