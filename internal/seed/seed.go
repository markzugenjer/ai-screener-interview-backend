// Package seed loads demo jobs with a Bahasa Indonesia question bank for junior programmers.
package seed

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/markzugenjer/ai-screener-interview-backend/internal/domain"
	"github.com/markzugenjer/ai-screener-interview-backend/internal/store"
)

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type q struct {
	skill, typ string
	diff       int
	text       string
	points     []string
	followUps  []string
}

var teamwork = q{"Kerja tim", "soft", 1,
	"Ceritakan pengalaman kamu bekerja dalam tim, lalu ada perbedaan pendapat. Bagaimana kamu menyelesaikannya?",
	[]string{"Menceritakan situasi yang spesifik", "Mendengarkan sudut pandang orang lain", "Berkomunikasi dengan sopan dan jelas", "Ada keputusan atau hasil yang dicapai", "Menyebutkan pelajaran yang didapat"},
	[]string{"Kalau terulang lagi, apa yang akan kamu lakukan berbeda?"}}

var motivation = q{"Motivasi", "soft", 1,
	"Kenapa kamu tertarik dengan posisi ini, dan apa yang ingin kamu pelajari dalam satu tahun ke depan?",
	[]string{"Ketertarikan yang tulus pada bidang ini", "Menghubungkan dengan pengalaman atau tujuan karier", "Punya target belajar yang konkret"},
	nil}

var gitQ = q{"Git", "technical", 1,
	"Apa perbedaan git merge dan git rebase? Kapan kamu memakai masing-masing?",
	[]string{"Merge membuat merge commit dan menjaga histori asli", "Rebase memindahkan commit ke atas branch lain sehingga histori linear", "Jangan rebase branch yang sudah dipakai bersama", "Menyebut cara menangani conflict"},
	[]string{"Apa yang kamu lakukan kalau terjadi merge conflict?"}}

var frontend = []q{
	{"JavaScript", "technical", 1, "Apa perbedaan let, const, dan var di JavaScript?",
		[]string{"var bersifat function-scoped dan di-hoist", "let dan const bersifat block-scoped", "const tidak bisa di-assign ulang, tapi isi object/array masih bisa diubah", "Menyebut temporal dead zone (nilai tambah)"},
		[]string{"Kalau const berisi array, apakah kita bisa melakukan push?"}},
	{"JavaScript", "technical", 2, "Jelaskan apa itu Promise dan bagaimana async/await bekerja.",
		[]string{"Promise mewakili nilai yang akan tersedia nanti", "State: pending, fulfilled, rejected", "Fungsi async selalu mengembalikan Promise", "await menunggu Promise selesai", "Error ditangani dengan try/catch atau .catch"},
		[]string{"Bagaimana cara menjalankan beberapa request secara paralel?"}},
	{"JavaScript", "technical", 3, "Bagaimana event loop di JavaScript bekerja?",
		[]string{"JavaScript berjalan single-threaded dengan call stack", "Callback masuk ke task queue", "Promise masuk ke microtask queue yang diproses lebih dulu", "Contoh urutan setTimeout dan Promise"},
		nil},
	{"React", "technical", 1, "Apa perbedaan props dan state di React?",
		[]string{"Props dikirim dari parent dan bersifat read-only", "State dikelola di dalam komponen", "Perubahan state memicu re-render", "Menyebut lifting state up"},
		nil},
	{"React", "technical", 2, "Kapan kamu memakai useEffect, dan apa fungsi dependency array?",
		[]string{"Untuk side effect seperti fetch data atau subscription", "Berjalan setelah render", "Dependency array menentukan kapan effect dijalankan ulang", "Array kosong berarti hanya sekali saat mount", "Cleanup function untuk membersihkan effect"},
		[]string{"Apa yang terjadi kalau dependency array tidak ditulis sama sekali?"}},
	{"React", "technical", 3, "Aplikasi React terasa lambat. Bagaimana cara kamu mengoptimalkan performanya?",
		[]string{"Mengukur dulu dengan React Profiler atau DevTools", "Mencegah re-render yang tidak perlu (memo, useMemo, useCallback)", "Code splitting dan lazy loading", "Virtualisasi untuk list yang panjang", "Key yang stabil pada list"},
		nil},
	{"CSS", "technical", 1, "Apa perbedaan Flexbox dan CSS Grid, dan kapan memakai masing-masing?",
		[]string{"Flexbox untuk layout satu dimensi", "Grid untuk layout dua dimensi (baris dan kolom)", "Flexbox cocok untuk alignment komponen", "Grid cocok untuk layout halaman"},
		nil},
	{"CSS", "technical", 2, "Bagaimana cara kamu membuat tampilan website yang responsive?",
		[]string{"Pendekatan mobile-first", "Media query atau breakpoint (misalnya sm, md, lg di Tailwind)", "Unit relatif seperti %, rem, vw", "Gambar yang fleksibel", "Menguji di berbagai ukuran layar"},
		nil},
	{"Web", "scenario", 2, "Ada halaman yang lambat sekali dimuat. Langkah apa saja yang kamu lakukan untuk mencari penyebabnya?",
		[]string{"Mereproduksi masalah", "Memakai DevTools: tab Network dan Performance, atau Lighthouse", "Memeriksa ukuran gambar dan aset", "Memeriksa ukuran bundle JavaScript", "Memeriksa waktu respon API", "Memperbaiki lalu mengukur ulang"},
		[]string{"Kalau ternyata penyebabnya gambar yang besar, apa solusinya?"}},
	gitQ, teamwork, motivation,
}

var backend = []q{
	{"Go", "technical", 1, "Apa perbedaan array dan slice di Go?",
		[]string{"Array punya panjang tetap dan panjangnya bagian dari tipe", "Slice adalah view dinamis di atas array", "Slice punya len dan cap", "append bisa mengalokasikan array baru"},
		nil},
	{"Go", "technical", 2, "Jelaskan goroutine dan channel di Go.",
		[]string{"Goroutine adalah thread ringan yang dikelola runtime Go", "Dijalankan dengan keyword go", "Channel untuk komunikasi dan sinkronisasi", "Buffered vs unbuffered channel", "Menghindari race condition"},
		[]string{"Bagaimana cara menunggu beberapa goroutine selesai?"}},
	{"Go", "technical", 2, "Bagaimana cara penanganan error yang baik di Go?",
		[]string{"Error adalah value yang dikembalikan fungsi", "Selalu cek if err != nil", "Membungkus error dengan fmt.Errorf dan %w", "Memakai errors.Is dan errors.As", "panic hanya untuk kondisi yang tidak bisa dipulihkan"},
		nil},
	{"SQL", "technical", 1, "Apa perbedaan INNER JOIN dan LEFT JOIN?",
		[]string{"INNER JOIN hanya mengambil baris yang cocok di kedua tabel", "LEFT JOIN mengambil semua baris tabel kiri", "Kolom tabel kanan bernilai NULL jika tidak ada pasangan", "Memberi contoh kasus"},
		nil},
	{"SQL", "technical", 2, "Apa itu index di database, dan kapan sebaiknya dipakai?",
		[]string{"Index mempercepat pencarian data", "Biasanya berbentuk B-tree", "Index menambah biaya saat insert/update dan butuh storage", "Dipakai pada kolom di WHERE, JOIN, atau ORDER BY", "Menyebut EXPLAIN untuk memeriksa query"},
		nil},
	{"REST API", "technical", 1, "Jelaskan fungsi HTTP method GET, POST, PUT, DELETE, dan beberapa status code yang umum.",
		[]string{"GET mengambil data, POST membuat data", "PUT memperbarui, DELETE menghapus", "2xx sukses, contoh 200 dan 201", "4xx kesalahan client, contoh 400, 401, 404", "5xx kesalahan server, contoh 500"},
		nil},
	{"Keamanan", "technical", 2, "Bagaimana cara mencegah SQL injection di aplikasi backend?",
		[]string{"Memakai parameterized query atau prepared statement", "Tidak menyambung input user langsung ke string SQL", "Validasi input", "Prinsip least privilege untuk user database"},
		nil},
	{"Backend", "scenario", 3, "API kamu tiba-tiba menjadi lambat di production. Apa yang kamu lakukan?",
		[]string{"Memeriksa log dan metrics", "Mencari endpoint yang lambat", "Memeriksa query database dengan EXPLAIN dan index", "Mempertimbangkan caching", "Mempertimbangkan rollback deploy terakhir", "Mengomunikasikan ke tim"},
		[]string{"Bagaimana cara kamu mencegah masalah yang sama terulang?"}},
	gitQ, teamwork, motivation,
}

func toQuestions(in []q) []domain.Question {
	out := make([]domain.Question, 0, len(in))
	for i, x := range in {
		fu := x.followUps
		if fu == nil {
			fu = []string{}
		}
		out = append(out, domain.Question{ID: uuid(), Skill: x.skill, Difficulty: x.diff, Type: x.typ,
			Text: x.text, ExpectedPoints: x.points, FollowUps: fu, Sort: i})
	}
	return out
}

// Demo inserts the demo jobs when the jobs table is empty. Safe to run repeatedly.
func Demo(ctx context.Context, s store.Store) (bool, error) {
	existing, err := s.ListJobs(ctx, store.JobFilter{})
	if err != nil {
		return false, err
	}
	if len(existing) > 0 {
		return false, nil
	}
	now := time.Now().UTC()
	jobs := []struct {
		job domain.Job
		qs  []q
	}{
		{domain.Job{
			Title: "Junior Frontend Developer", Slug: "junior-frontend-developer", Division: "Engineering",
			Category: "Technical", Level: "junior", Location: "Yogyakarta (Hybrid)", EmploymentType: "Full-time",
			Description:  "Build fast, accessible web interfaces with React, Next.js and Tailwind CSS together with our product and design team.",
			Requirements: []string{"Solid basics in HTML, CSS and JavaScript", "Has built at least one project with React", "Familiar with Git and teamwork", "Curious and eager to learn"},
			Skills:       []string{"react", "tailwind", "javascript"}, DurationMinutes: 20,
		}, frontend},
		{domain.Job{
			Title: "Junior Backend Developer (Go)", Slug: "junior-backend-developer-go", Division: "Engineering",
			Category: "Technical", Level: "junior", Location: "Remote (Indonesia)", EmploymentType: "Full-time",
			Description:  "Design and build reliable REST APIs in Go with PostgreSQL, and help keep our services fast and secure.",
			Requirements: []string{"Understands Go or another backend language", "Knows SQL and relational databases", "Understands REST API basics", "Cares about clean, tested code"},
			Skills:       []string{"go", "postgresql", "docker"}, DurationMinutes: 20,
		}, backend},
	}
	for i, x := range jobs {
		j := x.job
		j.ID = uuid()
		j.Status = domain.JobOpen
		j.Weights = domain.DefaultWeights()
		j.CreatedAt = now.Add(-time.Duration(i) * time.Minute)
		j.UpdatedAt = j.CreatedAt
		if err := s.CreateJob(ctx, &j); err != nil {
			return false, err
		}
		if err := s.ReplaceQuestions(ctx, j.ID, toQuestions(x.qs)); err != nil {
			return false, err
		}
	}
	return true, nil
}
