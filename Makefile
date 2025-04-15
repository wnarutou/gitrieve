.PHONY:dry-build
dry-build:
	go build -o gitrieve main.go
	rm -r gitrieve