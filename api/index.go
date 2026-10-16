package handler

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"lnzap/pkg/config"
	"lnzap/pkg/lnurl"
	"lnzap/pkg/server"
	"lnzap/pkg/splitkit"
	"lnzap/pkg/zap"
)

//go:embed config.yaml
var embeddedConfig []byte

var (
	setupOnce          sync.Once
	proxy              *server.Server
	setupErr           error
	httpClientOverride *http.Client
)

// Handler is the Vercel Go function entry point.
func Handler(w http.ResponseWriter, r *http.Request) {
	setupOnce.Do(load)
	if setupErr != nil {
		slog.Error("setup", "err", setupErr)
		writeSetupError(w)
		return
	}
	route := r.URL.Query().Get("lnzap_route")
	user := r.URL.Query().Get("lnzap_user")
	switch route {
	case "metadata":
		proxy.Metadata(w, r, user)
	case "callback":
		proxy.Callback(w, r, user, []string{"lnzap_route", "lnzap_user"})
	case "health":
		proxy.Health(w, r)
	default:
		writeRouteError(w)
	}
}

func load() {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		embedded, embedErr := config.Parse(embeddedConfig)
		if embedErr != nil {
			setupErr = err
			return
		}
		cfg = embedded
	}
	privateKey, err := zap.ParsePrivateKey(os.Getenv("NOSTR_PRIVATE_KEY"))
	if err != nil {
		setupErr = err
		return
	}
	client := lnurl.NewClient(cfg.CacheTTL.Std(), httpClientOverride)
	blocks := splitkit.NewOnDemand(cfg.SplitKitHost, cfg.SplitKitWait.Std(), cfg.SplitKitCacheTTL.Std())
	proxy = server.NewServer(cfg, client, privateKey, blocks)
}

func writeSetupError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ERROR",
		"reason": "service is not configured",
	})
}

func writeRouteError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ERROR",
		"reason": "unknown route",
	})
}
