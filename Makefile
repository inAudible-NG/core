dummy:
	@go build AA-ng.go
	@time ./AA-ng test.aa test.wav
	du -hs test.wav

	GOOS=windows GOARCH=amd64 go build -ldflags "-s" -o AA-ng-v19.exe AA-ng.go
	GOOS=darwin GOARCH=amd64 go build -ldflags "-s" -o AA-ng-v19.app AA-ng.go
deps:
	go get github.com/jteeuwen/audible 2>/dev/null || true
	go get golang.org/x/crypto 2>/dev/null || true
