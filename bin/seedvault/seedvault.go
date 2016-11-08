package main

import (
	"archive/zip"
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/thijzert/go-rcfile"
	tc "github.com/thijzert/go-termcolours"
	"github.com/thijzert/speeldoos"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

var (
	input_xml  = flag.String("input_xml", "", "Input XML file")
	output_dir = flag.String("output_dir", "seedvault", "Output directory")

	cover_image = flag.String("cover_image", "", "Path to cover image")
	inlay_image = flag.String("inlay_image", "", "Path to inlay image")
	eac_logfile = flag.String("eac_logfile", "", "Path to EAC log file")
	cuesheet    = flag.String("cuesheet", "", "Path to cuesheet")

	tracker_url = flag.String("tracker", "", "URL to private tracker")

	do_archive = flag.Bool("archive", true, "Create a speeldoos archive")
	do_320     = flag.Bool("320", false, "Also encode MP3-320")
	do_v0      = flag.Bool("v0", false, "Also encode V0")
	do_v2      = flag.Bool("v2", false, "Also encode V2")

	conc_jobs = flag.Int("j", 2, "Number of concurrent jobs")
)

var zm = &ZipMap{}

func init() {
	rcfile.Parse()
	flag.Parse()

	if *conc_jobs < 1 {
		*conc_jobs = 1
	}

	confirmSettings()
}

func confirmSettings() {
	fmt.Printf("\nAbout to start an encode with the following settings:\n")

	fmt.Printf("\nInput XML file: %s %s\n", fileExists(*input_xml), *input_xml)
	fmt.Printf("Output directory: %s\n", *output_dir)

	fmt.Printf("\nCover image:  %s %s\n", fileExists(*cover_image), *cover_image)
	fmt.Printf("Inlay image:  %s %s\n", fileExists(*inlay_image), *inlay_image)
	fmt.Printf("EAC log file: %s %s\n", fileExists(*eac_logfile), *eac_logfile)
	fmt.Printf("Cue sheet:    %s %s\n", fileExists(*cuesheet), *cuesheet)

	fmt.Printf("\nURL to private tracker: %s\n", tc.Blue(*tracker_url))

	fmt.Printf("\nEncodes to run: FLAC    %s\n", yes(true))
	fmt.Printf("                MP3-320 %s\n", yes(*do_320))
	fmt.Printf("                MP3-V0  %s\n", yes(*do_v0))
	fmt.Printf("                MP3-V2  %s\n", yes(*do_v2))
	fmt.Printf("                archive %s\n", yes(*do_archive))
	fmt.Printf("Number of concurrent encoding processes:  %d\n", *conc_jobs)

	fmt.Printf("\nIf the above looks good, hit <enter> to continue.\n")
	fmt.Printf("Otherwise, hit Ctrl+C to cancel the process.\n")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

func fileExists(filename string) string {
	if filename == "" {
		return tc.Bblack("(not specified)")
	}

	_, err := os.Stat(filename)
	if err == nil {
		return tc.Green("\u2713")
	}

	return tc.Red("not found")
}

func yes(i bool) string {
	if i {
		return tc.Green("yes")
	}
	return tc.Bblack("no")
}

func main() {
	foo, err := speeldoos.ImportCarrier(*input_xml)
	if err != nil {
		log.Fatal(err)
	}

	track_counter := 0

	log.Printf("Initiating .flac run...")

	albus := newAlbum(foo.Name)
	for _, pf := range foo.Performances {
		for _, p := range pf.Performers {
			if p.Name != "" {
				albus.Name = p.Name + " - " + foo.Name
				break
			}
		}
		if albus.Name != foo.Name {
			break
		}
	}

	croak(os.Mkdir(*output_dir, 0755))
	croak(os.Mkdir(path.Join(*output_dir, cleanFilename(albus.Name)+" [FLAC]"), 0755))

	for _, pf := range foo.Performances {
		title := "(no title)"
		if len(pf.Work.Title) > 0 {
			title = pf.Work.Title[0].Title
		}

		log.Printf("Now processing: %s - %s", pf.Work.Composer.Name, title)

		if len(pf.SourceFiles) == 0 {
			log.Printf("%s - %s has no source files!\n", pf.Work.Composer.Name, title)
			continue
		}

		for i, sf := range pf.SourceFiles {
			fn := sf.Filename
			track_counter++
			fileTitle := title
			if len(pf.Work.Parts) > 1 && len(pf.Work.Parts) > i {
				fileTitle = fmt.Sprintf("%s - %d. %s", title, i+1, pf.Work.Parts[i])
			}
			out := fmt.Sprintf("%02d - %s - %s", track_counter, pf.Work.Composer.Name, fileTitle)

			mm := &mFile{
				Basename: out,
				Title:    fileTitle,
				Album:    foo.Name,
				Composer: pf.Work.Composer.Name,
				Year:     pf.Year,
				Track:    track_counter,
			}

			for _, p := range pf.Performers {
				if mm.Artist == "" {
					mm.Artist = p.Name
				}

				if (p.Role == "soloist" || p.Role == "performer") && mm.Soloist == "" {
					mm.Soloist = p.Name
				} else if (p.Role == "orchestra" || p.Role == "ensemble") && mm.Orchestra == "" {
					mm.Orchestra = p.Name
				} else if p.Role == "conductor" && mm.Conductor == "" {
					mm.Conductor = p.Name
				}
			}

			albus.Add(mm)

			out = path.Join(*output_dir, cleanFilename(albus.Name)+" [FLAC]", cleanFilename(out)+".flac")

			f, err := os.Create(out)
			croak(err)
			g, err := zm.Get(fn)
			croak(err)

			_, err = io.Copy(f, g)
			croak(err)
			f.Close()
			g.Close()

			cmd := exec.Command("metaflac", "--remove-all-tags", "--no-utf8-convert", "--import-tags-from=-", out)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			pipeIn, err := cmd.StdinPipe()
			croak(err)
			cmd.Start()

			writeFlacTag(pipeIn, "title", mm.Title)
			writeFlacTag(pipeIn, "artist", mm.Artist)
			writeFlacTag(pipeIn, "album", mm.Album)
			writeFlacTag(pipeIn, "tracknumber", fmt.Sprintf("%d", mm.Track))
			writeFlacTag(pipeIn, "composer", mm.Composer)
			for _, p := range pf.Performers {
				writeFlacTag(pipeIn, "performer", p.Name)
			}
			writeFlacTag(pipeIn, "date", fmt.Sprintf("%d", mm.Year))
			writeFlacTag(pipeIn, "genre", "classical") // FIXME
			pipeIn.Close()
			croak(cmd.Wait())
		}
	}
	log.Printf("FLAC run complete")

	if *do_v2 || *do_v0 || *do_320 {
		log.Printf("Decoding to WAV...")
		croak(os.Mkdir(path.Join(*output_dir, "wav"), 0755))
		albus.Job("FLAC", func(mf *mFile, out, in string) []*exec.Cmd {
			// HACK: Decoding is achieved by working in the flac/ dir and swapping the input and output parameters
			return []*exec.Cmd{exec.Command("flac", "-d", "-s", "-o", out, in+".flac")}
		})
		log.Printf("Done decoding.")
	} else {
		albus.Job("FLAC", func(_ *mFile, _, _ string) []*exec.Cmd {
			return []*exec.Cmd{exec.Command("true")}
		})
	}

	if *do_v2 {
		log.Printf("Encoding V2 profile...")
		albus.Job("V2", lameRun("-V2", "--vbr-new"))
		log.Printf("Done encoding.")
	}

	if *do_v0 {
		log.Printf("Encoding V0 profile...")
		albus.Job("V0", lameRun("-V0", "--vbr-new"))
		log.Printf("Done encoding.")
	}

	if *do_320 {
		log.Printf("Encoding 320 profile...")
		albus.Job("320", lameRun("-b", "320"))
		log.Printf("Done encoding.")
	}

	if *do_archive {
		archive_name := foo.ID
		if archive_name == "" {
			archive_name = "speeldoos"
		}
		archive_name = cleanFilename(archive_name)
		archive_name = strings.Replace(archive_name, " ", "-", -1)
		c := exec.Command("zip", "--quiet", "-r", "-Z", "store", path.Join("..", archive_name+".zip"), ".")
		c.Dir = path.Join(*output_dir, cleanFilename(albus.Name)+" [FLAC]")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		c.Start()
		croak(c.Wait())

		bar, err := speeldoos.ImportCarrier(*input_xml)
		croak(err)

		h := sha256.New()
		f, err := os.Open(path.Join(*output_dir, archive_name+".zip"))
		croak(err)
		_, err = io.Copy(h, f)
		croak(err)

		bar.Hash = "sha256-" + hex.EncodeToString(h.Sum(nil))

		sourcefile_counter := 0
		for i, pf := range bar.Performances {
			for j, _ := range pf.SourceFiles {
				bar.Performances[i].SourceFiles[j].Filename = path.Join(archive_name+".zip", cleanFilename(albus.Tracks[sourcefile_counter].Basename)+".flac")
				sourcefile_counter++
			}
		}

		bar.Write(path.Join(*output_dir, archive_name+".xml"))
	}
}

type ZipMap struct {
	zips map[string]*zip.ReadCloser
}

func (z *ZipMap) Get(filename string) (io.ReadCloser, error) {
	rv, _ := os.Open(os.DevNull)

	if z.zips == nil {
		z.zips = make(map[string]*zip.ReadCloser)
	}

	var err error

	// Try opening the file itself, maybe that works...
	fi, err := os.Stat(filename)
	if err == nil {
		// Is it a regular file?
		if (fi.Mode() & os.ModeType) == 0 {
			return os.Open(filename)
		}
	}

	// FIXME: I'm of the opinion that this should work: elems := filepath.SplitList(filename)
	elems := strings.Split(filename, "/")
	for i, elem := range elems {
		if len(elem) < 5 || elem[len(elem)-4:] != ".zip" {
			continue
		}
		zipfile := filepath.Join(elems[0 : i+1]...)
		read, ok := z.zips[zipfile]
		if !ok {
			log.Printf("Opening zip file %s...\n", zipfile)
			read, err = zip.OpenReader(zipfile)
			if err != nil {
				log.Print(err)
				read = nil
			}
			z.zips[zipfile] = read
		}

		if read == nil {
			continue
		}

		localfile := filepath.Join(elems[i+1:]...)

		for _, zfp := range read.File {
			if zfp.Name == localfile {
				return zfp.Open()
			}
		}

		return rv, os.ErrNotExist
	}

	return rv, os.ErrNotExist
}

func croak(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

func cleanFilename(value string) string {
	value = strings.Replace(value, "\n", " ", -1)
	value = strings.Replace(value, "\r", " ", -1)
	value = strings.Replace(value, "\t", " ", -1)
	value = strings.Replace(value, "\x00", " ", -1)
	value = strings.Replace(value, "=", "", -1)
	value = strings.Replace(value, "\"", "", -1)
	value = strings.Replace(value, ":", "", -1)
	value = strings.Replace(value, "?", "", -1)
	value = strings.Replace(value, "/", "-", -1)

	return value
}

func writeFlacTag(f io.Writer, key, value string) {
	if value == "" {
		return
	}
	value = strings.Replace(value, "\n", " ", -1)
	value = strings.Replace(value, "\r", " ", -1)
	value = strings.Replace(value, "\t", " ", -1)
	value = strings.Replace(value, "\x00", " ", -1)
	value = strings.Replace(value, "=", "-", -1)

	d := fmt.Sprintf("%s=%s\n", strings.ToUpper(key), value)

	f.Write([]byte(d))
}

type jobFun func(*mFile, string, string) []*exec.Cmd

type mFile struct {
	Basename                                string
	Title, Artist, Album                    string
	Composer, Soloist, Orchestra, Conductor string
	Year, Track                             int
}

type album struct {
	Name   string
	Tracks []*mFile
}

func newAlbum(name string) *album {
	return &album{
		Name:   name,
		Tracks: make([]*mFile, 0, 20),
	}
}

func (a *album) Add(mm *mFile) {
	a.Tracks = append(a.Tracks, mm)
}

func (a *album) Job(dir string, fun jobFun) {
	working_dir := path.Join(*output_dir, cleanFilename(a.Name)+" ["+dir+"]")
	croak(os.MkdirAll(working_dir, 0755))

	cmds := make(chan []*exec.Cmd)
	var wg sync.WaitGroup

	for i := 0; i < *conc_jobs; i++ {
		go func() {
			wg.Add(1)
			for cmdList := range cmds {
				for _, cmd := range cmdList {
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Start()
					croak(cmd.Wait())
				}
			}
			wg.Done()
		}()
	}

	for _, mf := range a.Tracks {
		in := path.Join(*output_dir, "wav", mf.Basename+".wav")
		out := path.Join(working_dir, cleanFilename(mf.Basename))

		cmds <- fun(mf, in, out)
	}

	close(cmds)

	if *cover_image != "" {
		f, err := zm.Get(*cover_image)
		if err != nil {
			log.Print(err)
		} else {
			g, err := os.Create(path.Join(working_dir, "folder.jpg"))
			croak(err)
			_, err = io.Copy(g, f)
			croak(err)
		}
	}

	if *inlay_image != "" {
		f, err := zm.Get(*inlay_image)
		if err != nil {
			log.Print(err)
		} else {
			g, err := os.Create(path.Join(working_dir, "inlay.jpg"))
			croak(err)
			_, err = io.Copy(g, f)
			croak(err)
		}
	}

	if *cuesheet != "" {
		f, err := zm.Get(*cuesheet)
		if err != nil {
			log.Print(err)
		} else {
			g, err := os.Create(path.Join(working_dir, "cuesheet.cue"))
			croak(err)
			_, err = io.Copy(g, f)
			croak(err)
		}
	}

	if *eac_logfile != "" {
		f, err := zm.Get(*eac_logfile)
		if err != nil {
			log.Print(err)
		} else {
			g, err := os.Create(path.Join(working_dir, "eac.log"))
			croak(err)
			_, err = io.Copy(g, f)
			croak(err)
		}
	}

	wg.Wait()

	if *tracker_url != "" {
		// working_dir := path.Join(*output_dir, a.Name+" ["+dir+"]")
		cmd := exec.Command("mktorrent", "-a", *tracker_url, "-p", working_dir, "-o", working_dir+".torrent")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Start()
		croak(cmd.Wait())
	}
}

func lameRun(extraArgs ...string) jobFun {
	return func(s *mFile, in, out string) []*exec.Cmd {
		cmdline := []string{"--quiet", "--replaygain-accurate", "--id3v2-only"}

		cmdline = append(cmdline, extraArgs...)
		cmdline = append(cmdline, in, out+".mp3")

		return []*exec.Cmd{
			exec.Command("lame", cmdline...),
			id3tags(s, in, out),
		}
	}
}

func id3tags(s *mFile, in, out string) *exec.Cmd {
	args := []string{
		"-t", s.Title,
		"-a", s.Artist,
		"--TCOM", s.Composer,
		"--genre", "32",
	}
	if s.Album != "" {
		args = append(args, "-A", s.Album)
	}

	if s.Year != 0 {
		args = append(args, "-y", fmt.Sprintf("%d", s.Year))
	}
	if s.Track != 0 {
		args = append(args, "-T", fmt.Sprintf("%d", s.Track))
	}

	if s.Soloist != "" {
		args = append(args, "--TPE1", s.Soloist)
	}
	if s.Orchestra != "" {
		args = append(args, "--TPE2", s.Orchestra)
	}
	if s.Conductor != "" {
		args = append(args, "--TPE3", s.Conductor)
	}

	args = append(args, out+".mp3")

	return exec.Command("id3v2", args...)
}
