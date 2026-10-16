module github.com/womat/manchester/demo/manchester_listener

go 1.27

require (
	github.com/womat/gpio v1.0.2
	github.com/womat/manchester v1.0.0
)

require (
	github.com/warthog618/go-gpiocdev v0.10.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/womat/manchester => ../..
