package listeners

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/iocdi"
	"github.com/Station-Manager/logging"
	"github.com/Station-Manager/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type TestSuite struct {
	suite.Suite
}

var container *iocdi.Container

func (suite *TestSuite) SetupTest() {
	container = iocdi.New()
	err := container.Register(config.ServiceName, reflect.TypeOf(&config.Service{}))
	assert.NoError(suite.T(), err)

	err = container.RegisterInstance(logging.ServiceName, &logging.Service{})
	assert.NoError(suite.T(), err)

	err = container.Register(ServiceName, reflect.TypeOf(&Service{}))
	assert.NoError(suite.T(), err)

	path, err := filepath.Abs(filepath.Join("..", "build"))
	assert.NoError(suite.T(), err)

	err = os.Setenv(utils.EnvSmWorkingDir, path)
	assert.NoError(suite.T(), err)

	workDir, err := utils.WorkingDir()
	assert.NoError(suite.T(), err)

	fmt.Println("Working Directory:", workDir)

	err = container.RegisterInstance("workingdir", workDir)
	assert.NoError(suite.T(), err)

	err = container.Build()
	assert.NoError(suite.T(), err)
}

func TestListenerTestSuite(t *testing.T) {
	suite.Run(t, new(TestSuite))
}

func (suite *TestSuite) TestInit() {
	service, err := container.ResolveSafe(ServiceName)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), service)
}

func (suite *TestSuite) TestStart() {
	service, err := container.ResolveSafe(ServiceName)
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), service)
	err = service.(*Service).Start(context.Background())
	assert.NoError(suite.T(), err)
	err = service.(*Service).Start(context.Background())
	assert.NoError(suite.T(), err)

	time.Sleep(2000 * time.Millisecond)

	err = service.(*Service).Stop()
	assert.NoError(suite.T(), err)
}
