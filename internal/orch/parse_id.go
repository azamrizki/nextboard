package orch

import (
	"regexp"
	"strings"
)

// Indonesian commands (docs/API-orchestration.md §3.2). The owner types them as often as
// English ones, and the planner would cost a turn for "jeda semua". Each rule turns the frame
// of an Indonesian command into the English grammar, keeping names and the text for agents
// exactly as typed; what is left goes to the planner as before. Words that are as often a
// remark to the lead as a command ("lanjut", "tunggu", "lihat", "hapus") are left out on
// purpose: a wrong command costs more than a planner turn.

type idRule struct {
	re *regexp.Regexp
	to func(m []string) string
}

func idre(p string) *regexp.Regexp { return regexp.MustCompile(`(?i)^` + p + `$`) }

func lit(s string) func([]string) string { return func([]string) string { return s } }

// opt is " "+s, or nothing for an empty part.
func opt(prefix, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return prefix + s
}

var idPanels = map[string]string{"memori": "memory", "memory": "memory", "pengaturan": "settings", "setelan": "settings", "git": "git",
	"usage": "usage", "pemakaian": "usage", "dokumen": "documents", "kode": "code", "code": "code", "markdown": "markdown",
	"skill": "skills", "skills": "skills", "loop": "loops", "loops": "loops", "peta misi": "mission map"}

var idTiles = map[string]string{"checklist": "checklist", "daftar periksa": "checklist", "kanban": "kanban", "kalender": "calendar", "uptime": "uptime"}

var idThanks = idre(`(makasih|terima ?kasih|trims|tengkyu)( ya| banyak)?[ !.]*`)

var idRules = []idRule{
	{idThanks, lit("thanks")},
	{idre(`(oke|sip|siap|mantap|bagus|keren)( ya)?[ !.]*`), lit("ok")},
	{idre(`(batal|batalkan|tidak jadi|gak jadi|nggak jadi|ga jadi|gajadi)`), lit("cancel that")},
	{idre(`(perbesar|zoom masuk)`), lit("zoom in")},
	{idre(`(perkecil|zoom keluar)`), lit("zoom out")},
	{idre(`(tampilkan semua(nya)?|lihat semua(nya)?|muat (ke )?layar|pas(kan)? (ke )?layar)`), lit("fit")},
	{idre(`(fokus ke|fokus pada|pergi ke) (.+)`), func(m []string) string { return "focus on " + m[2] }},
	{idre(`buka (?:panel )?(memori|memory|pengaturan|setelan|git|usage|pemakaian|dokumen|kode|code|markdown|skills?|loops?|peta misi)(?: panel)?`), func(m []string) string {
		return "open " + idPanels[strings.ToLower(m[1])]
	}},
	{idre(`tutup (panel|panelnya)`), lit("close the panel")},
	{idre(`(?:pindah ke|buka) workspace (.+)`), func(m []string) string { return "go to workspace " + m[1] }},
	{idre(`(?:buat|tambah(?:kan)?)(?: sebuah)? workspace(?: baru)?(?: (?:bernama|namanya|dengan nama) (.+))?`), func(m []string) string {
		return "create workspace" + opt(" called ", m[1])
	}},
	{idre(`(?:ganti|ubah) nama workspace(?: ini)? (?:jadi|menjadi) (.+)`), func(m []string) string { return "rename this workspace to " + m[1] }},
	{idre(`tutup workspace (.+)`), func(m []string) string { return "close workspace " + m[1] }},
	{idre(`tambah(?:kan)? catatan(?: (?:yang )?(?:berisi|isinya|bertuliskan):? (.+))?`), func(m []string) string {
		return "add a note" + opt(" that says ", m[1])
	}},
	{idre(`tambah(?:kan)? (checklist|daftar periksa|kanban|kalender|uptime)(?: baru)?`), func(m []string) string {
		return "add a " + idTiles[strings.ToLower(m[1])]
	}},
	{idre(`buka (?:http://)?localhost:? ?(\d{2,5})`), func(m []string) string { return "open localhost " + m[1] }},
	{idre(`(?:setiap|tiap) (?:(\S+) )?(menit|jam) (.+)`), func(m []string) string {
		unit := "minutes"
		if strings.EqualFold(m[2], "jam") {
			unit = "hours"
		}
		return "every " + opt("", m[1]+" ") + unit + " " + m[3]
	}},
	{idre(`commit (?:dan|lalu) push`), lit("commit and push")},
	{idre(`commit dengan pesan:? (.+)`), func(m []string) string { return "commit with message " + m[1] }},
	{idre(`(?:kirim|push) memori(?: ke (.+))?`), func(m []string) string { return "push memory" + opt(" to ", m[1]) }},
	{idre(`(?:scan|pindai) memori`), lit("scan memory")},
	{idre(`(status|statusnya|gimana statusnya|bagaimana statusnya|gimana progresnya|bagaimana progresnya|sudah sampai mana)\??`), lit("status")},
	{idre(`(siapa yang (?:sudah )?selesai|apa yang terjadi)\??`), lit("who finished")},
	{idre(`(siapa yang (?:butuh|perlu|nungguin|menunggu) (?:aku|saya)|siapa yang menunggu)\??`), lit("who needs me")},
	{idre(`(ringkasan|rangkum|rangkumkan|buat ringkasan|buatkan ringkasan)\??`), lit("summary")},
	{idre(`(bantuan|bisa apa(?: saja)?|perintah apa saja)\??`), lit("help")},
	{idre(`(buka terminal(?: baru)?|terminal baru|buka shell|shell baru)`), lit("open a terminal")},
	{idre(`(buka konsol(?: baru)?|konsol baru|console baru)`), lit("new console")},
	{idre(`(?:buat|buatkan|bikin) (?:grup|kelompok|tim)(?: (?:berisi|dari|beranggotakan))? (\S+)(?: (.+?))?(?: (?:bernama|namanya|dengan nama) (.+))?`), func(m []string) string {
		return "make a group of " + m[1] + opt(" ", m[2]) + opt(" called ", m[3])
	}},
	{idre(`tambah(?:kan)? (?:orkestrator|orchestrator)(?: untuk (.+))?`), func(m []string) string { return "add an orchestrator" + opt(" for ", m[1]) }},
	{idre(`tambah(?:kan)? (\S+)(?: (\S+))? ke (?:dalam )?(?:grup |tim |kelompok )?(.+)`), func(m []string) string {
		return "add " + m[1] + opt(" ", m[2]) + " to " + m[3]
	}},
	{idre(`(?:hubungkan|sambungkan) (.+?) ke (?:orkestrator|orchestrator)(?: (.+))?`), func(m []string) string {
		return "connect " + m[1] + " to the orchestrator" + opt(" ", m[2])
	}},
	{idre(`urungkan`), lit("undo")},
	{idre(`(?:(?:aku|saya) )?(?:mau|ingin) (?:gambar|menggambar)`), lit("pen")},
	{idre(`penghapus`), lit("eraser")},
	{idre(`(?:terapkan|pasang) (?:layout|tata letak) (.+)`), func(m []string) string { return "deploy " + m[1] }},
	{idre(`(?:buat|buatkan|jalankan|nyalakan|tambah|tambahkan) (.+)`), func(m []string) string {
		if !idWantsConsole(m[1]) {
			return ""
		}
		return "spawn " + m[1]
	}},
	{idre(`(?:kelompokkan|gabungkan|jadikan grup) (ini|mereka|yang dipilih|pilihan(?:nya)?)`), lit("group these")},
	{idre(`(?:kelompokkan|gabungkan) (.+)`), func(m []string) string { return "group " + m[1] }},
	{idre(`(?:bubarkan|pisahkan) (.+)`), func(m []string) string { return "dissolve " + m[1] }},
	{idre(`(tunggu dulu|tahan dulu|stop dulu)`), lit("wait")},
	{idre(`(?:jeda|tahan|hentikan sementara)(?: (.+))?`), func(m []string) string { return "pause" + opt(" ", m[1]) }},
	{idre(`(?:lanjutkan|lanjut|teruskan) (.+)`), func(m []string) string { return "resume " + m[1] }},
	{idre(`(?:mulai ulang|muat ulang|restart) (.+)`), func(m []string) string { return "restart " + m[1] }},
	{idre(`(?:matikan|hentikan|tutup) (semua|semuanya|semua agen|semua console|semua konsol)`), lit("kill all")},
	{idre(`(?:beri|kasih) semua(?:nya)? (?:prompt|perintah) (?:yang )?sama:? (.+)`), func(m []string) string { return "give everyone the same prompt: " + m[1] }},
	{idre(`(?:bilang|katakan|suruh|minta) (?:ke |kepada )?semua(?:nya)?(?: (?:untuk|agar|supaya))?:? (.+)`), func(m []string) string { return "tell everyone: " + m[1] }},
	{idre(`(?:matikan|hentikan|tutup) (.+)`), func(m []string) string { return "kill " + m[1] }},
	{idre(`(?:serahkan|alihkan|oper)(?: (?:tugas )?ini)? ke @?(\S+?)(?: dari @?(\S+))?`), func(m []string) string {
		return "hand off to " + m[1] + opt(" from ", m[2])
	}},
	{idre(`(?:kalau|jika|setelah|begitu) (.+?) (?:selesai|beres|kelar),? (?:teruskan|kirim|oper|serahkan)(?: (?:hasilnya|hasil(?: kerja)?(?:nya)?))? ke (.+)`), func(m []string) string {
		return "when " + m[1] + " finishes, pass it to " + m[2]
	}},
	{idre(`(?:suruh|minta|perintahkan|bilang (?:ke|kepada)|katakan (?:ke|kepada)|tolong (?:suruh|minta)) (.+?) (?:untuk|agar|supaya) (.+)`), func(m []string) string {
		return "tell " + m[1] + " to " + m[2]
	}},
	// "minta tolong cek login" has no addressee: the address form lets a word that is no name go
	// to the planner, with or without one
	{idre(`(?:suruh|minta) @?(\S+) (.+)`), func(m []string) string { return m[1] + ", " + m[2] }},
}

// idWantsConsole tells a request for a console from a request for work. After "spawn" a role or
// a filler word is enough; after "buat" or "jalankan" it is not: "buat dokumentasi", "jalankan
// backend", "jalankan lagi" and "buat cli" ask for work. It takes a number, a CLI, or the word
// for a console; spawnTokens refuses anything else that is left.
func idWantsConsole(rest string) bool {
	for _, w := range strings.Fields(strings.ToLower(rest)) {
		switch w {
		case "lagi", "baru", "buah", "new", "more", "of", "code", "cli", "session", "sessions", "instance", "instances":
		default:
			if RoleOf(w) == "" {
				return true
			}
		}
	}
	return false
}

// indonesian rewrites an Indonesian command into the English grammar, or gives "".
func indonesian(t string) string {
	for _, r := range idRules {
		if m := r.re.FindStringSubmatch(t); m != nil {
			return r.to(m)
		}
	}
	return ""
}
