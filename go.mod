module apitool

go 1.27.0

tool github.com/vivaui/viva/cmd/viva

require github.com/vivaui/viva v0.6.5


require (
	github.com/dsnet/compress v0.0.0-20171208185109-cc9eb1d7ad76 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/gabstv/go-bsdiff v1.0.5 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
)

replace github.com/vivaui/viva => ./third_party/viva
