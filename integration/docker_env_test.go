package integration_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

type integrationEnv struct {
	pool          *dockertest.Pool
	mysqlResource *dockertest.Resource
	mysqlDSN      string
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

	return env
}

func (e *integrationEnv) close() {
	if e == nil || e.pool == nil {
		return
	}
	if e.mysqlResource != nil {
		_ = e.pool.Purge(e.mysqlResource)
	}
}
