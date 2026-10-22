package config

const defaultPort = "8080"
const defaultResultsMode = "s3"

type Config struct {
	Port        string
	ResultsMode string
}