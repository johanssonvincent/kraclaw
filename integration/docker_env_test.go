package integration_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/moby/moby/api/types/container"
	natserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/ory/dockertest/v4"
)

type integrationEnv struct {
	pool          dockertest.ClosablePool
	mysqlResource dockertest.ClosableResource
	mysqlDSN      string
	natsServer    *natserver.Server
	natsStoreDir  string
	natsConn      *nats.Conn
	setupErr      error
}

var (
	envOnce sync.Once
	envInst *integrationEnv
)

func TestMain(m *testing.M) {
	code := m.Run()
	if envInst != nil {
		envInst.close()
	}
	os.Exit(code)
}

func requireIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration tests in -short mode")
	}

	envOnce.Do(func() {
		envInst = setupIntegrationEnv()
	})

	if envInst.setupErr != nil {
		t.Skipf("skipping integration tests: %v", envInst.setupErr)
	}

	return envInst
}

func setupIntegrationEnv() *integrationEnv {
	env := &integrationEnv{}
	ctx := context.Background()

	pool, err := dockertest.NewPool(ctx, "", dockertest.WithMaxWait(2*time.Minute))
	if err != nil {
		env.setupErr = fmt.Errorf("create docker pool: %w", err)
		return env
	}
	env.pool = pool

	env.mysqlResource, err = pool.Run(ctx, "mysql",
		dockertest.WithTag("8.0"),
		dockertest.WithEnv([]string{
			"MYSQL_ROOT_PASSWORD=kraclaw",
			"MYSQL_DATABASE=kraclaw_test",
		}),
		dockertest.WithHostConfig(func(hc *container.HostConfig) {
			hc.AutoRemove = true
			hc.RestartPolicy = container.RestartPolicy{Name: "no"}
		}),
	)
	if err != nil {
		env.setupErr = fmt.Errorf("start mysql container: %w", err)
		return env
	}

	mysqlPort := env.mysqlResource.GetPort("3306/tcp")
	env.mysqlDSN = fmt.Sprintf("root:kraclaw@tcp(localhost:%s)/kraclaw_test?parseTime=true", mysqlPort)

	if err := pool.Retry(ctx, 2*time.Minute, func() error {
		db, err := sql.Open("mysql", env.mysqlDSN)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		return db.Ping()
	}); err != nil {
		env.setupErr = fmt.Errorf("wait for mysql: %w", err)
		env.close()
		return env
	}

	natsDir, err := os.MkdirTemp("", "kraclaw-nats-test-*")
	if err != nil {
		env.setupErr = fmt.Errorf("create nats store dir: %w", err)
		env.close()
		return env
	}
	env.natsStoreDir = natsDir

	natsOpts := &natserver.Options{
		JetStream: true,
		StoreDir:  natsDir,
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
	}
	ns, err := natserver.NewServer(natsOpts)
	if err != nil {
		env.setupErr = fmt.Errorf("create nats server: %w", err)
		env.close()
		return env
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		env.setupErr = fmt.Errorf("nats server not ready")
		env.close()
		return env
	}
	env.natsServer = ns

	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		env.setupErr = fmt.Errorf("connect to embedded nats: %w", err)
		env.close()
		return env
	}
	env.natsConn = nc

	return env
}

func (e *integrationEnv) close() {
	if e == nil || e.pool == nil {
		return
	}
	if e.natsConn != nil {
		e.natsConn.Close()
	}
	if e.natsServer != nil {
		e.natsServer.Shutdown()
	}
	if e.natsStoreDir != "" {
		_ = os.RemoveAll(e.natsStoreDir)
	}
	if e.mysqlResource != nil {
		_ = e.mysqlResource.Close(context.Background())
	}
	_ = e.pool.Close(context.Background())
}
