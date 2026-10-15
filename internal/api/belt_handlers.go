package api

import "net/http"

// GET /api/belt - пояс лиги: у кого он, сколько на нём ржавчины и вся история.
func (s *Server) handleBelt(w http.ResponseWriter, r *http.Request) {
	b, err := s.Store.Belt(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b)
}
