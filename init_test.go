package listeners

import (
	"reflect"
	"testing"

	"github.com/Station-Manager/config"
	"github.com/Station-Manager/iocdi"
	"github.com/Station-Manager/logging"
	"github.com/Station-Manager/utils"
)

func TestInit(t *testing.T) {
	container := iocdi.New()
	err := container.Register(config.ServiceName, reflect.TypeOf(&config.Service{}))
	if err != nil {
		t.Fatal(err)
	}
	err = container.RegisterInstance(logging.ServiceName, &logging.Service{})
	if err != nil {
		t.Fatal(err)
	}
	err = container.Register(ServiceName, reflect.TypeOf(&Service{}))
	if err != nil {
		t.Fatal(err)
	}
	workDir, err := utils.WorkingDir()
	if err != nil {
		t.Fatal(err)
	}
	err = container.RegisterInstance("workingdir", workDir)
	if err != nil {
		t.Fatal(err)
	}
	err = container.Build()
	if err != nil {
		t.Fatal(err)
	}

	service, err := container.ResolveSafe(ServiceName)
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("service is nil")
	}
}
