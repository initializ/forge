module github.com/initializ/forge/forge-ui

go 1.25.0

require (
	github.com/initializ/forge/forge-core v0.0.0
	github.com/initializ/forge/forge-skills v0.0.0
)

require (
	github.com/xeipuuv/gojsonpointer v0.0.0-20180127040702-4e3ac2762d5f // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/initializ/forge/forge-core => ../forge-core
	github.com/initializ/forge/forge-skills => ../forge-skills
)
