package cfb_test

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/abemedia/go-cfb"
)

func ExampleWriter() {
	// Create a file to write our compound file to.
	f, err := os.CreateTemp("", "example-*.cfb")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	// Create a new compound file.
	w := cfb.NewWriterV3(f)

	// Add some streams to the compound file.
	files := []struct {
		Name, Body string
	}{
		{"readme.txt", "This archive contains some text files."},
		{"gopher.txt", "Gopher names:\nGeorge\nGeoffrey\nGonzo"},
		{"todo.txt", "Get animal handling licence.\nWrite more examples."},
	}
	for _, file := range files {
		s, err := w.CreateStream(file.Name)
		if err != nil {
			log.Fatal(err)
		}
		_, err = s.Write([]byte(file.Body))
		if err != nil {
			log.Fatal(err)
		}
		if err := s.Close(); err != nil {
			log.Fatal(err)
		}
	}

	// Make sure to check the error on Close.
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	// Output:
}

func ExampleReader() {
	// Open a compound file for reading.
	r, err := cfb.OpenReader("testdata/example.cfb")
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()

	// Iterate through the streams in the compound file,
	// printing some of their contents.
	for _, e := range r.Entries {
		s, ok := e.(*cfb.Stream)
		if !ok {
			continue
		}
		fmt.Printf("Contents of %s:\n", s.Name)
		_, err = io.Copy(os.Stdout, s.Open())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println()
	}
	// Output:
	// Contents of README.md:
	// This is an example CFB file.
}
