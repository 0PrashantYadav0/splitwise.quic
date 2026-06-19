package handlers

import (
	"net/http"

	"splitwise-quic/internal/models"
)

type expensesView struct {
	GroupID  string
	Expenses []models.Expense
}

func (h *Handlers) expensesPartial(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	exp, err := h.store.Expenses(g.ID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.render.Partial(w, "expenses", expensesView{GroupID: g.ID, Expenses: exp})
}

type balancesView struct {
	GroupID   string
	Balances  map[string][]models.Balance
	Transfers map[string][]models.Transfer
}

func (h *Handlers) balancesPartial(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	balances, err := h.store.Balances(g.ID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	transfers, err := h.store.SimplifiedTransfers(g.ID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.render.Partial(w, "balances", balancesView{
		GroupID: g.ID, Balances: balances, Transfers: transfers,
	})
}

type activityView struct {
	Activities []models.Activity
}

func (h *Handlers) activityPartial(w http.ResponseWriter, r *http.Request) {
	g := h.requireMember(w, r)
	if g == nil {
		return
	}
	acts, err := h.store.Activities(g.ID, 25)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.render.Partial(w, "activity", activityView{Activities: acts})
}
