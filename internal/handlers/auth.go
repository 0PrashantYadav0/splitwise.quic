package handlers

import (
	"net/http"
	"strings"
)

type loginView struct {
	view
	Error string
}

func (h *Handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	if h.currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.renderLogin(w, "")
}

func (h *Handlers) renderLogin(w http.ResponseWriter, errMsg string) {
	data := loginView{view: view{Title: "Log in", CertHash: h.certHash}, Error: errMsg}
	if err := h.render.Page(w, "login", data); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handlers) doSignup(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	name := strings.TrimSpace(r.FormValue("name"))
	pw := r.FormValue("password")
	if email == "" || name == "" || len(pw) < 6 {
		h.renderLogin(w, "Name, email and a 6+ char password are required.")
		return
	}
	u, err := h.store.CreateUser(email, name, pw)
	if err != nil {
		h.renderLogin(w, "Could not create account (email may already be in use).")
		return
	}
	h.startSession(w, u.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) doLogin(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	pw := r.FormValue("password")
	u, err := h.store.Authenticate(email, pw)
	if err != nil {
		h.renderLogin(w, "Invalid email or password.")
		return
	}
	h.startSession(w, u.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handlers) doLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("sid"); err == nil {
		_ = h.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: "sid", Value: "", Path: "/", MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *Handlers) startSession(w http.ResponseWriter, userID string) {
	token, err := h.store.CreateSession(userID)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "sid",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30,
	})
}
