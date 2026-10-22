package config

const defaultPort = "8080"
const defaultResultsMode = "local"

type Config struct {
	Port        string
	ResultsMode string
}

func Default() Config {
	return Config{
		Port:        defaultPort,
		ResultsMode: defaultResultsMode,
	}
}
