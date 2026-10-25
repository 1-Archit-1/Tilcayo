package config

// This file holds the values you are most likely to change: defaults, limits
// and the list of engines. The logic that uses them lives in config.go.
//
// To add an engine: add a line to `engines` below (and an EngineXxx constant
// if other code wants to refer to it by name), plus workers/<name>/Dockerfile.
// TILCAYO_IMAGE_<NAME> and the resource defaults then work with no other change.

// Version can be set at build time via -ldflags "-X ...".
var Version = "dev"

// Engine names, as sent in POST /jobs.
const (
	EngineGeneral = "general"
	EngineSpatial = "spatial"
	EngineML      = "ml"
)

// Allowed values for the mode settings.
const (
	OrchestratorDocker     = "docker"
	OrchestratorKubernetes = "kubernetes"

	ResultsS3    = "s3"
	ResultsLocal = "local"

	URLStyleVhost = "vhost"
	URLStylePath  = "path"
)

const (
	giB int64 = 1024 * 1024 * 1024
	miB int64 = 1024 * 1024
)

// engineSpec holds what differs between engines.
type engineSpec struct {
	DefaultMemory int64 // bytes, used when TILCAYO_JOB_MEMORY is not set
}

// engines is the single list of known engines. Anything not in it is
// rejected with ErrUnknownEngine.
var engines = map[string]engineSpec{
	EngineGeneral: {DefaultMemory: 1 * giB},
	EngineSpatial: {DefaultMemory: 1 * giB},
	EngineML:      {DefaultMemory: 4 * giB}, // provisional (item 13 measures it)
}

// Application defaults.
const (
	DefaultPort        = "8080"
	DefaultResultsMode = ResultsS3
	DefaultURLStyle    = URLStyleVhost
	DefaultCPU         = 1.0 // cores, every engine
)

// Allowed range for TILCAYO_JOB_CPU (cores) and TILCAYO_JOB_MEMORY (bytes).
// The floors keep a typo from becoming "no limit" or a limit Docker refuses.
const (
	minCPU    = 0.01
	maxCPU    = 256
	minMemory = 64 * miB
	maxMemory = 1024 * giB // 1 Ti
)

// Worker image used when TILCAYO_IMAGE_<ENGINE> is not set: the engine name
// and Version fill the two %s.
const defaultImageFormat = "ghcr.io/1-archit-1/tilcayo-worker-%s:%s"
