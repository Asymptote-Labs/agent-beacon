package dashboard

import (
	"net/http"
	"strconv"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/learning"
)

func ReadMemory(logPath string, query learning.Query) (MemoryResponse, error) {
	store := learning.Open(learning.PathForRuntimeLog(logPath))
	status, err := store.Status()
	if err != nil {
		return MemoryResponse{}, err
	}
	evaluations, err := store.ListEvaluations(query)
	if err != nil {
		return MemoryResponse{}, err
	}
	candidates, err := store.ListCandidates(query)
	if err != nil {
		return MemoryResponse{}, err
	}
	memories, err := store.ListMemories(query)
	if err != nil {
		return MemoryResponse{}, err
	}
	return MemoryResponse{
		Status:      status,
		Evaluations: evaluations,
		Candidates:  candidates,
		Memories:    memories,
	}, nil
}

func parseMemoryQuery(r *http.Request) learning.Query {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 {
		page = 1
	}
	return learning.Query{
		ProjectID:   q.Get("project_id"),
		ProjectPath: q.Get("project"),
		State:       q.Get("state"),
		Kind:        q.Get("kind"),
		Q:           q.Get("q"),
		Limit:       limit,
		Page:        page,
	}
}
