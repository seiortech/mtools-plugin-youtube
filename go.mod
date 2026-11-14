module github.com/seiortech/mtools-plugin-youtube

go 1.27.3

require (
	github.com/egoist/mygo v0.8.3
	github.com/seiortech/mtools-sdk v0.1.0
	github.com/seiortech/mtools-sdk/ui v0.1.0
)

replace (
	github.com/seiortech/mtools-sdk => ../mtools-sdk
	github.com/seiortech/mtools-sdk/ui => ../mtools-sdk/ui
)
