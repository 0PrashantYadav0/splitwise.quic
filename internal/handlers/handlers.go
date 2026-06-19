// Package handlers contains all HTTP request handlers and routing.
package handlers

import (
	"context"
	"net/http"

	"splitwise-quic/internal/models"
	"splitwise-quic/internal/realtime"
	"splitwise-quic/internal/render"
	"splitwise-quic/internal/server"
	"splitwise-quic/internal/store"
)

type ctxKey int

const userKey ctxKey = iota

// Handlers bundles every dependency the HTTP layer needs.
type Handlers struct {
	store    *store.Store
	render   *render.Renderer
	hub      *realtime.Hub
	srv      *server.Server // for WebTransport upgrades + cert hash
	certHash string
}

// New constructs the handler set (srv is wired in Routes).
func New(s *store.Store, r *render.Renderer, h *realtime.Hub) *Handlers {
	return &Handlers{store: s, render: r, hub: h}
}

// Routes wires every endpoint and returns the top-level handler.
// It takes the *server.Server so the WebTransport endpoint can upgrade.
func (h *Handlers) Routes(srv *server.Server) http.Handler {
	h.srv = srv
	h.certHash = srv.CertHashB64()

	mux := http.NewServeMux()
	mux.Handle("GET /static/", render.StaticHandler())

	// Auth
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.doLogin)
	mux.HandleFunc("POST /signup", h.doSignup)
	mux.HandleFunc("GET /logout", h.doLogout)

	// App (auth-required)
	mux.Handle("GET /{$}", h.auth(h.dashboard))
	mux.Handle("POST /groups", h.auth(h.createGroup))
	mux.Handle("GET /g/{id}", h.auth(h.groupPage))
	mux.Handle("POST /g/{id}/members", h.auth(h.addMember))
	mux.Handle("POST /g/{id}/expenses", h.auth(h.createExpense))
	mux.Handle("GET /g/{id}/expenses", h.auth(h.expensesPartial))
	mux.Handle("POST /g/{id}/expenses/{eid}/delete", h.auth(h.deleteExpense))
	mux.Handle("GET /g/{id}/balances", h.auth(h.balancesPartial))
	mux.Handle("GET /g/{id}/activity", h.auth(h.activityPartial))
	mux.Handle("POST /g/{id}/settle", h.auth(h.settle))
	mux.Handle("GET /g/{id}/events", h.auth(h.sse))

	// WebTransport (QUIC datagrams) - auth via session cookie inside handler.
	mux.HandleFunc("/g/{id}/wt", h.webTransport)

	return mux
}

// --- auth middleware + helpers -------------------------------------------

func (h *Handlers) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := h.currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

func (h *Handlers) currentUser(r *http.Request) *models.User {
	c, err := r.Cookie("sid")
	if err != nil {
		return nil
	}
	u, err := h.store.UserBySession(c.Value)
	if err != nil {
		return nil
	}
	return u
}

func userFrom(r *http.Request) *models.User {
	u, _ := r.Context().Value(userKey).(*models.User)
	return u
}

// view is the base data every page template needs.
type view struct {
	Title    string
	User     *models.User
	CertHash string
}

func (h *Handlers) base(r *http.Request, title string) view {
	return view{Title: title, User: userFrom(r), CertHash: h.certHash}
}

func httpError(w http.ResponseWriter, msg string, code int) {
	http.Error(w, msg, code)
}
