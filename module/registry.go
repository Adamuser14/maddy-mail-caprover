package module

import "sync"

var modules = make(map[string]NewModule)
var modulesLck sync.RWMutex

var ModuleDirectives []string

// Register adds module factory function to global registry.
//
// name must be unique. Register will panic if module with specified name
// already exists in registry.
//
// Contents of cfgDirectives will be appended ModuleDirectives. You should include
// all used config directives here, otherwise configuration file will not be parsed
// correctly.
//
// You probably want to call this function from func init() of module package.
func Register(name string, factory NewModule, cfgDirectives []string) {
	modulesLck.Lock()
	defer modulesLck.Unlock()

	if _, ok := modules[name]; ok {
		panic("Register: module with specified name is already registered: " + name)
	}

	ModuleDirectives = append(ModuleDirectives, cfgDirectives...)

	modules[name] = factory
}

// GetMod returns module from global registry.
//
// Nil is returned if no module with specified name is registered.
func GetMod(name string) NewModule {
	modulesLck.RLock()
	defer modulesLck.RUnlock()

	return modules[name]
}

var instances = make(map[string]Module)
var instancesLck sync.RWMutex

// Register adds module factory function to global registry.
//
// name must be unique. Register will panic if module with specified name
// already exists in registry.
//
// You probably want to call this function from func init() of module package.
func RegisterInstance(inst Module) {
	instancesLck.Lock()
	defer instancesLck.Unlock()

	if _, ok := instances[inst.InstanceName()]; ok {
		panic("RegisterInstance: instance with specified name is already registered: " + inst.InstanceName())
	}

	instances[inst.InstanceName()] = inst
}

// GetInstance returns module instance from global registry.
//
// Nil is returned if no module instance with specified name is registered.
func GetInstance(name string) Module {
	instancesLck.RLock()
	defer instancesLck.RUnlock()

	return instances[name]
}
