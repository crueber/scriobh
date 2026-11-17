package imports_test

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// A 2 MiB file of nothing but headings must not become 400,000 scenes.
func TestAFileOfHeadingsIsNotSplitIntoAMillionScenes(t *testing.T) {
	e := newEnv(t)
	flood := strings.Repeat("## x\n", 2<<20/5) // 419,430 headings in 2 MiB
	f := []imports.File{
		{Path: "01-flood.md", Data: []byte(flood)},
		{Path: "02-fine.md", Data: []byte("# Fine\n\n## One\n\na\n\n## Two\n\nb\n")},
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: f, SplitScenes: "h2"}).(imports.PreviewOut)
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if pv.Documents > imports.MaxDocuments {
		t.Fatalf("the plan holds %d documents, over the limit of %d", pv.Documents, imports.MaxDocuments)
	}
	// The flood stays whole; the small file still splits.
	if pv.Documents != 4 || pv.SkippedByReason["too many headings to split into scenes"] != 1 {
		t.Errorf("documents %d, skipped %v", pv.Documents, pv.SkippedByReason)
	}
	if !strings.Contains(strings.Join(pv.Warnings, " "), "kept whole") {
		t.Errorf("warnings: %v", pv.Warnings)
	}
	// (Before the cap this plan held 419,000 nodes and about 750 MB.)
	memBudgetMB := uint64(400)
	if timingFactor > 1 {
		memBudgetMB = 2000 // the race detector inflates allocation
	}
	mb := (after.TotalAlloc - before.TotalAlloc) >> 20
	if mb > memBudgetMB {
		t.Errorf("preview allocated %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second*timingFactor {
			t.Errorf("preview allocated %d MB in %v", mb, took)
		}
	})
	// Apply makes exactly what the preview showed.
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: f, SplitScenes: "h2"}).(imports.ApplyOut)
	if out.Documents != 4 {
		t.Errorf("apply made %d documents", out.Documents)
	}
	// Many files together cannot get around it either.
	var many []imports.File
	for i := 0; i < 16; i++ {
		many = append(many, imports.File{Path: fmt.Sprintf("%02d.md", i), Data: []byte(flood)})
	}
	pv = e.must(e.w, "import.preview", imports.PreviewIn{Files: many, SplitScenes: "h2"}).(imports.PreviewOut)
	if pv.Documents != 16 || len(pv.Nodes) > imports.MaxDocuments {
		t.Errorf("sixteen floods: %d documents, %d nodes", pv.Documents, len(pv.Nodes))
	}
}

// A listing can hold a.md and a folder named a.md/: they must not collide.
func TestAFileAndAFolderWithTheSameNameDoNotCollide(t *testing.T) {
	e := newEnv(t)
	f := files(map[string]string{"a.md": "The file.\n", "a.md/01-inside.md": "Inside.\n"})
	pv := e.preview(f, "")
	keys := map[string]bool{}
	for _, n := range pv.Nodes {
		if keys[n.Key] {
			t.Errorf("duplicate key %q", n.Key)
		}
		keys[n.Key] = true
	}
	eq(t, outline(pv.Nodes), []string{"chapter:A", "part:A.md", "  chapter:Inside"})
	out := e.apply(f, "", nil)
	if out.Documents != 3 {
		t.Errorf("documents %d", out.Documents)
	}
	// The inside chapter really sits inside the part.
	var inside int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents c JOIN documents p ON p.id = c.parent_id WHERE c.project_id = ? AND c.title = 'Inside' AND p.kind = 'part'`, out.ProjectID).Scan(&inside)
	if inside != 1 {
		t.Errorf("the nested chapter lost its parent")
	}
}

// The declared entry count and directory size cannot be trusted: archive/zip
// reads headers from the declared offset until one stops matching.
func TestZipDirectoryIsWalkedNotTrusted(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 60_000; i++ {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%d.md", i), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_ = w
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	eocd := len(b) - 22
	// Say there are 5 entries in a 100-byte directory; the real one is 60,000.
	binary.LittleEndian.PutUint16(b[eocd+10:], 5)
	binary.LittleEndian.PutUint32(b[eocd+12:], 100)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := imports.ReadZip(b)
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("a directory of 60,000 entries with a doctored end record: %v", err)
	}
	if mb := (after.TotalAlloc - before.TotalAlloc) >> 20; mb > 8 {
		t.Errorf("refusing it allocated %d MB", mb)
	}
	// Honest over-limit counts, and the zip64 triggers, are refused too.
	good := func() []byte {
		var g bytes.Buffer
		z := zip.NewWriter(&g)
		w, _ := z.Create("a.md")
		w.Write([]byte("A.\n"))
		z.Close()
		return g.Bytes()
	}
	g := good()
	if _, err := imports.ReadZip(g); err != nil {
		t.Fatal(err)
	}
	e := len(g) - 22
	claim := func(entries uint16, size, offset uint32) []byte {
		x := append([]byte{}, g...)
		binary.LittleEndian.PutUint16(x[e+10:], entries)
		binary.LittleEndian.PutUint32(x[e+12:], size)
		binary.LittleEndian.PutUint32(x[e+16:], offset)
		return x
	}
	off := binary.LittleEndian.Uint32(g[e+16:])
	size := binary.LittleEndian.Uint32(g[e+12:])
	for name, z := range map[string][]byte{
		"entries": claim(60000, size, off), "zip64 entries": claim(0xFFFF, size, off),
		"zip64 size": claim(1, 0xFFFFFFFF, off), "zip64 offset": claim(1, size, 0xFFFFFFFF),
	} {
		if _, err := imports.ReadZip(z); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Applying many large documents must not parse them all at once.
func TestApplyOfManyLargeFilesHasABoundedPeak(t *testing.T) {
	e := newEnv(t)
	flood := strings.Repeat("## x\n", 2<<20/5)
	var many []imports.File
	for i := 0; i < 8; i++ {
		many = append(many, imports.File{Path: fmt.Sprintf("%02d.md", i), Data: []byte(flood)})
	}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: many}).(imports.PreviewOut)
	var peak uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	runtime.GC()
	e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: many})
	close(stop)
	<-done
	t.Logf("peak heap while applying 8 x 2 MiB: %d MB", peak>>20)
	if peak>>20 > 800 {
		t.Errorf("peak heap %d MB", peak>>20)
	}
}
