package platform

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/draas-platform/draas/internal/catalog"
	"github.com/draas-platform/draas/internal/storage"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"os"
	"time"
)

type Principal struct {
	Tenant string `json:"tenant"`
	Role   string `json:"role"`
	Actor  string `json:"actor"`
	Token  string `json:"token"`
}
type Config struct {
	DatabaseURL, NATSURL, S3Endpoint, S3Access, S3Secret, S3Bucket, Listen, Brand string
	S3TLS                                                                         bool
	Master, Seed                                                                  []byte
	Principals                                                                    []Principal
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func LoadConfig() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), NATSURL: os.Getenv("NATS_URL"), S3Endpoint: os.Getenv("S3_ENDPOINT"), S3Access: os.Getenv("S3_ACCESS_KEY"), S3Secret: os.Getenv("S3_SECRET_KEY"), S3Bucket: env("S3_BUCKET", "draas-backups"), S3TLS: env("S3_TLS", "true") == "true", Listen: env("LISTEN_ADDR", "127.0.0.1:8080"), Brand: env("BRAND_NAME", "DraaS")}
	var err error
	c.Master, err = hex.DecodeString(os.Getenv("MASTER_KEY"))
	if err != nil || len(c.Master) != 32 {
		return c, errors.New("MASTER_KEY must contain 32 hex-encoded bytes")
	}
	c.Seed, err = hex.DecodeString(os.Getenv("SIGNING_SEED"))
	if err != nil || len(c.Seed) != 32 {
		return c, errors.New("SIGNING_SEED must contain 32 hex-encoded bytes")
	}
	if c.DatabaseURL == "" || c.NATSURL == "" || c.S3Endpoint == "" || c.S3Access == "" || c.S3Secret == "" {
		return c, errors.New("database, NATS and S3 configuration required")
	}
	if err = json.Unmarshal([]byte(os.Getenv("AUTH_PRINCIPALS")), &c.Principals); err != nil || len(c.Principals) == 0 {
		return c, errors.New("AUTH_PRINCIPALS JSON required")
	}
	seen := map[string]bool{}
	for _, p := range c.Principals {
		if !backup.ValidID(p.Tenant) || len(p.Token) < 32 || p.Actor == "" || (p.Role != "admin" && p.Role != "operator" && p.Role != "viewer") || seen[p.Token] {
			return c, errors.New("invalid principal configuration")
		}
		seen[p.Token] = true
	}
	return c, nil
}

type App struct {
	Config Config
	DB     *catalog.DB
	Engine *backup.Engine
	NC     *nats.Conn
	JS     nats.JetStreamContext
	Store  *storage.S3
	Traces *trace.TracerProvider
}

func Open(ctx context.Context, c Config) (*App, error) {
	db, err := catalog.Open(ctx, c.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err = db.Migrate(ctx); err != nil {
		db.Pool.Close()
		return nil, err
	}
	for _, p := range c.Principals {
		if err = db.SeedTenant(ctx, p.Tenant); err != nil {
			db.Pool.Close()
			return nil, err
		}
	}
	st, err := storage.New(c.S3Endpoint, c.S3Access, c.S3Secret, c.S3Bucket, c.S3TLS)
	if err != nil {
		db.Pool.Close()
		return nil, err
	}
	if err = st.Ensure(ctx); err != nil {
		db.Pool.Close()
		return nil, err
	}
	nc, err := nats.Connect(c.NATSURL, nats.Name("draas"), nats.Timeout(5*time.Second), nats.MaxReconnects(-1))
	if err != nil {
		db.Pool.Close()
		return nil, err
	}
	js, err := nc.JetStream(nats.MaxWait(5 * time.Second))
	if err != nil {
		db.Pool.Close()
		nc.Close()
		return nil, err
	}
	if _, err = js.AddStream(&nats.StreamConfig{Name: "DRAAS_JOBS", Subjects: []string{"draas.jobs.*.v1"}, Storage: nats.FileStorage, Retention: nats.WorkQueuePolicy, MaxAge: 30 * 24 * time.Hour, MaxBytes: 256 << 20, Discard: nats.DiscardNew, Duplicates: 10 * time.Minute}); err != nil {
		db.Pool.Close()
		nc.Close()
		return nil, err
	}
	signer := ed25519.NewKeyFromSeed(c.Seed)
	tp := trace.NewTracerProvider(trace.WithBatcher(spanLogExporter{}, trace.WithMaxQueueSize(512), trace.WithExportTimeout(5*time.Second)))
	otel.SetTracerProvider(tp)
	return &App{Config: c, DB: db, Engine: &backup.Engine{Store: st, Master: c.Master, Signer: signer, Trusted: signer.Public().(ed25519.PublicKey)}, NC: nc, JS: js, Store: st, Traces: tp}, nil
}
func (a *App) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a.Traces != nil {
		_ = a.Traces.Shutdown(ctx)
	}
	a.NC.Close()
	a.DB.Pool.Close()
}
