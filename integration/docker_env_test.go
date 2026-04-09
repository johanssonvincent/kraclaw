package integration_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	nats "github.com/nats-io/nats.go"
	natserver "github.com/nats-io/nats-server/v2/server"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

type integrationEnv struct {
	pool          *dockertest.Pool
	mysqlResource *dockertest.Resource
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

	pool, err := dockertest.NewPool("")
	if err != nil {
		env.setupErr = fmt.Errorf("create docker pool: %w", err)
		return env
	}
	pool.MaxWait = 2 * time.Minute
	env.pool = pool

	env.mysqlResource, err = pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "mysql",
		Tag:        "8.0",
		Env: []string{
			"MYSQL_ROOT_PASSWORD=kraclaw",
			"MYSQL_DATABASE=kraclaw_test",
		},
	}, func(hc *docker.HostConfig) {
		hc.AutoRemove = true
		hc.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		env.setupErr = fmt.Errorf("start mysql container: %w", err)
		return env
	}

	mysqlPort := env.mysqlResource.GetPort("3306/tcp")
	env.mysqlDSN = fmt.Sprintf("root:kraclaw@tcp(localhost:%s)/kraclaw_test?parseTime=true", mysqlPort)

	if err := pool.Retry(func() error {
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

	// Start embedded NATS with JetStream for IPC and queue tests.
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
		_ = e.pool.Purge(e.mysqlResource)
	}
}
