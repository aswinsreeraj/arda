package registry

import (
	"database/sql"
	"net/http"

	registryhttp "github.com/aswinsreeraj/arda/services/registry/http"
	"github.com/aswinsreeraj/arda/services/registry/service"
	"github.com/aswinsreeraj/arda/services/registry/store"
	_ "github.com/lib/pq"
)

// Server wraps the Agent Registry HTTP service, dependencies, and database connection.
type Server struct {
	Handler http.Handler
	Store   store.Store
	Service *service.RegistryService
	DB      *sql.DB
}

// NewServer initializes a new Agent Registry Server with dependencies injected.
func NewServer(db *sql.DB, resolver registryhttp.TenantResolver) *Server {
	st := store.NewPostgresStore(db)
	svc := service.NewRegistryService(st)
	h := registryhttp.NewHandler(svc)
	handler := registryhttp.RegisterRoutes(h, resolver)

	return &Server{
		Handler: handler,
		Store:   st,
		Service: svc,
		DB:      db,
	}
}
