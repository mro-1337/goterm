module goterm

go 1.23.0

require (
	golang.org/x/crypto v0.38.0
	golang.org/x/sys v0.33.0
)

replace golang.org/x/crypto => ./third_party/crypto

replace golang.org/x/sys => ./third_party/sys

require (
	github.com/hajimehoshi/go-mp3 v0.0.0
	github.com/jfreymuth/oggvorbis v0.0.0
	github.com/jfreymuth/vorbis v0.0.0
)

replace github.com/hajimehoshi/go-mp3 => ./third_party/mp3

replace github.com/jfreymuth/oggvorbis => ./third_party/oggvorbis

replace github.com/jfreymuth/vorbis => ./third_party/vorbis
