module github.com/initializ/forge/forge-cli

go 1.25.0

require (
	github.com/initializ/forge/forge-core v0.0.0
	github.com/initializ/forge/forge-plugins v0.0.0
	github.com/manifoldco/promptui v0.9.0
	github.com/spf13/cobra v1.10.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/chzyer/readline v0.0.0-20180603132655-2972be24d48e // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/xeipuuv/gojsonpointer v0.0.0-20180127040702-4e3ac2762d5f // indirect
	github.com/xeipuuv/gojsonreference v0.0.0-20180127040603-bd5ef7bd5415 // indirect
	github.com/xeipuuv/gojsonschema v1.2.0 // indirect
	golang.org/x/sys v0.0.0-20181122145206-62eef0e2fa9b // indirect
)

replace (
	github.com/initializ/forge/forge-core => ../forge-core
	github.com/initializ/forge/forge-plugins => ../forge-plugins
)
