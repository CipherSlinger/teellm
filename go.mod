module github.com/CipherSlinger/teellm

go 1.22

require github.com/CipherSlinger/teetls v0.1.0

require (
	github.com/tjfoc/gmsm v1.4.1 // indirect
	golang.org/x/crypto v0.24.0 // indirect
	golang.org/x/sys v0.21.0 // indirect
)

replace github.com/CipherSlinger/teetls => ./teetls
