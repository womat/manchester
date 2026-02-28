module github.com/womat/golib/demo/manchester_sender

go 1.26.0

require (
	github.com/womat/golib/gpio v0.0.0-00010101000000-000000000000
	github.com/womat/golib/manchester/encoder v0.0.0-00010101000000-000000000000
)

require (
	github.com/warthog618/go-gpiocdev v0.9.1 // indirect
	golang.org/x/sys v0.41.0 // indirect
)

replace github.com/womat/golib/gpio => ../../gpio

replace github.com/womat/golib/manchester/encoder => ../../manchester/encoder
