package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

// Team & access: admins decide who can use the recruiter area. Changes apply
// on the user's next request (sessions load the user each time) and are
// audited. An admin cannot change their own role, and the last admin cannot
// be demoted, so the team can never lock itself out.

type teamMemberDTO struct {
	ID          string     `json:"id"`
	FullName    string     `json:"fullName"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	ViaJobi     bool       `json:"viaJobi"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

func toTeamMember(u *domain.User) teamMemberDTO {
	email := u.Email
	if strings.HasSuffix(email, "@jobi.invalid") {
		email = "" // placeholder for JOBI accounts without a verified email
	}
	return teamMemberDTO{ID: u.ID, FullName: u.FullName, Email: email, Role: string(u.Role), ViaJobi: u.JobiSub != "",
		CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt}
}

var roles = []string{string(domain.RoleCandidate), string(domain.RoleRecruiter), string(domain.RoleAdmin)}

func (a *API) adminListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	role := q.Get("role")
	if role != "" && !slices.Contains(roles, role) {
		role = ""
	}
	users, total, err := a.store.ListUsers(r.Context(), store.UserFilter{
		Query: truncateStr(strings.TrimSpace(q.Get("q")), 80), Role: role, Page: page, PageSize: 25})
	if err != nil {
		a.log.Error("list users", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not load the team.")
		return
	}
	counts := map[string]int{}
	for _, rl := range roles {
		if n, err := a.store.CountUsersByRole(r.Context(), domain.Role(rl)); err == nil {
			counts[rl] = n
		}
	}
	out := make([]teamMemberDTO, 0, len(users))
	for i := range users {
		out = append(out, toTeamMember(&users[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "total": total, "page": page, "pageSize": 25,
		"counts": counts, "openRecruiterAccess": a.cfg.OpenRecruiterAccess, "me": userFrom(r).ID})
}

func (a *API) adminSetUserRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	me := userFrom(r)
	var in struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !slices.Contains(roles, in.Role) {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be candidate, recruiter or admin.")
		return
	}
	if !validUUID(id) {
		writeError(w, http.StatusNotFound, "not_found", "User not found.")
		return
	}
	if id == me.ID {
		writeError(w, http.StatusConflict, "own_role", "You can't change your own role. Ask another admin.")
		return
	}
	u, err := a.store.GetUserByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "User not found.")
		return
	}
	newRole := domain.Role(in.Role)
	if u.Role == newRole {
		writeJSON(w, http.StatusOK, map[string]any{"user": toTeamMember(u)})
		return
	}
	if u.Role == domain.RoleAdmin {
		if n, err := a.store.CountUsersByRole(r.Context(), domain.RoleAdmin); err != nil || n <= 1 {
			writeError(w, http.StatusConflict, "last_admin", "This is the last admin. Make someone else admin first.")
			return
		}
	}
	if err := a.store.SetUserRole(r.Context(), id, newRole); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "User not found.")
			return
		}
		a.log.Error("set role", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Could not change the role.")
		return
	}
	a.audit(r, "user.role", "user", id, map[string]any{"from": string(u.Role), "to": in.Role})
	u.Role = newRole
	writeJSON(w, http.StatusOK, map[string]any{"user": toTeamMember(u)})
}
