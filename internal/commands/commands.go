package commands

import (
	"reflect"

	"github.com/celestix/gotgproto/dispatcher"
	"go.uber.org/zap"
)

// Definimos la estructura una sola vez para todo el paquete
type command struct {
	log *zap.Logger
}

// Load inicializa todos los handlers del paquete commands
func Load(log *zap.Logger, dispatcher dispatcher.Dispatcher) {
	log = log.Named("commands")
	defer log.Info("All command handlers have been initialized")

	// Instanciamos la estructura con el logger
	cmdInstance := &command{log: log}
	
	Type := reflect.TypeOf(cmdInstance)
	Value := reflect.ValueOf(cmdInstance)

	// Iteramos sobre todos los métodos vinculados a la estructura 'command'
	for i := 0; i < Type.NumMethod(); i++ {
		method := Type.Method(i)
		// Saltamos el cargador para no entrar en recursión si tuviera la misma firma
		if method.Name == "Load" {
			continue
		}
		method.Func.Call([]reflect.Value{Value, reflect.ValueOf(dispatcher)})
	}
}
