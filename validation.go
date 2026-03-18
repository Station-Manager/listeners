package listeners

import (
	"sync"

	"github.com/Station-Manager/types"
	"github.com/go-playground/validator/v10"
)

var validate *validator.Validate
var once sync.Once

func validateConfig(cfg *types.ListenerConfig) error {
	once.Do(func() {
		validate = validator.New(validator.WithRequiredStructEnabled())
	})

	return validate.Struct(cfg)
}
