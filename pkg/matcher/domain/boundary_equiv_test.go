package domain

import (
	"math/rand"
	"regexp"
	"testing"
)

func TestBoundaryMatcherEquivalentGenerated(t *testing.T) {
	patterns := []string{
		"(^|\\.)13mei[5-7]\\.buzz$",
		"(^|\\.)18j[efg]\\.life$",
		"(^|\\.)18jmttios[0-9]{2}\\.com$",
		"(^|\\.)18tv[1-5]\\.com$",
		"(^|\\.)1[0-9]{2}\\.one$",
		"(^|\\.)2024(3[0-9]|4[0-9]|6[0-9]|7[0-9])\\.xyz$",
		"(^|\\.)3[0-9]{2}\\.icu$",
		"(^|\\.)3[0-9]{2}\\.one$",
		"(^|\\.)69bag[0-1][1-9]?\\.com$",
		"(^|\\.)783([a-z][a-z]?)?\\.tv$",
		"(^|\\.)7mm0([0-1][0-9]?)?\\.com$",
		"(^|\\.)8dgo[1-57-9]\\.com$",
		"(^|\\.)91cg([1-9]|[0-9][1-9])?.com$",
		"(^|\\.)91p[0-9][0-9]?\\.com$",
		"(^|\\.)91porn[0-9]?\\.store$",
		"(^|\\.)91porn[0-9]{3}\\.me$",
		"(^|\\.)91porn[fvxy]\\.com$",
		"(^|\\.)91porn\\.(best|com|cool|fun|group|party|plus|site|tw|work)$",
		"(^|\\.)91porna[1-7]?\\.com$",
		"(^|\\.)91sew1[1-6]\\.buzz$",
		"(^|\\.)91wang[1-9]\\.com$",
		"(^|\\.)989[a-z]?\\.tv$",
		"(^|\\.)99pp[1-9][0-9]\\.com$",
		"(^|\\.)9cha[0-9]{2}\\.cc$",
		"(^|\\.)[1-5]gaoap\\.com$",
		"(^|\\.)[1-9]+aipai\\.(com|net)$",
		"(^|\\.)[1-9]+zipai\\.(com|net)$",
		"(^|\\.)[1-9]00caocao\\.com$",
		"(^|\\.)[3-9]0000sao\\.com$",
		"(^|\\.)[a-z]?fulibl\\.(cc|com|org|net|xyz)$",
		"(^|\\.)[a-z][1-9][0-9][a-z]\\.com$",
		"(^|\\.)aipa(111|222|333|444|555|666|777|888|999)\\.life$",
		"(^|\\.)aisejushi(2|3|5|6)\\.com$",
		"(^|\\.)aiwei[1-9]{2}\\.buzz$",
		"(^|\\.)apiproxy-device-prod-nlb-.+\\.amazonaws\\.com$",
		"(^|\\.)apiproxy-website-nlb-prod-.+\\.amazonaws\\.com$",
		"(^|\\.)aqdk[0-9]{3}\\.com$",
		"(^|\\.)avcc1[0-9]\\.com$",
		"(^|\\.)avstar0[1-9]\\.(com|me)$",
		"(^|\\.)awjd[1-9]?\\.tv$",
		"(^|\\.)awwcn[1-9]?\\.top$",
		"(^|\\.)banyinjia[0-9]?\\.com$",
		"(^|\\.)bhzyk[1-9]?\\.xyz$",
		"(^|\\.)bibeidh1[6-8]\\.cc$",
		"(^|\\.)bilibili3(0[1-9]|1[0-2])\\.xyz$",
		"(^|\\.)bjini[0-9]{2}\\.buzz$",
		"(^|\\.)byy[0-9][0-9]?\\.icu$",
		"(^|\\.)byyum([3589]|2[235689]|3[34]|4[1-9]|5[1-79]|6[0134679])?\\.com$",
		"(^|\\.)cav1(0[2-9]|1[0-9])\\.com$",
		"(^|\\.)cgbb([1-9]|1[0-9]|20)\\.com$",
		"(^|\\.)chuzs[1-9]{0,2}\\.buzz$",
		"(^|\\.)cosaa[a-z]\\.cyou$",
		"(^|\\.)doufuru[0-9]?\\.cc$",
		"(^|\\.)doufuru[0-9]{2}?\\.xyz$",
		"(^|\\.)douyintt[0-9]\\.me$",
		"(^|\\.)dualstack\\.apiproxy-.+\\.amazonaws\\.com$",
		"(^|\\.)dualstack\\.ichnaea-web-.+\\.amazonaws\\.com$",
		"(^|\\.)fby[1-9]?[0-9]\\.icu$",
		"(^|\\.)fby[1-9]?[0-9]\\.one$",
		"(^|\\.)fi11sm([0-9]+)?\\.com$",
		"(^|\\.)fiftymvapi\\..+$",
		"(^|\\.)flsq[0-9]{2}\\.com$",
		"(^|\\.)forduck[0-1][0-9]\\.com$",
		"(^|\\.)gaogediao\\.(com|me|net|org)$",
		"(^|\\.)gossipfuli[0-9]{3,4}\\.xyz$",
		"(^|\\.)guifw3[1-3]\\.cc$",
		"(^|\\.)h5ccav[1-5]\\.com$",
		"(^|\\.)hhmen[1-3]\\.xyz$",
		"(^|\\.)hkfg[1-7]\\.xyz$",
		"(^|\\.)hs[1-9]{2}\\.vip$",
		"(^|\\.)jav777\\.(cc|tv|work)$",
		"(^|\\.)jb[1-9][0-9]\\.lol$",
		"(^|\\.)jbk0([0-1][0-9]?)?\\.com$",
		"(^|\\.)jdyy[1-9]?\\.me$",
		"(^|\\.)jiuse[0-9]{1,3}\\.com$",
		"(^|\\.)jmtt\\.(app|live|tv|vip)$",
		"(^|\\.)jstv[0-9]{2}\\.com$",
		"(^|\\.)kdfl[0-9]{2}\\.xyz$",
		"(^|\\.)kimoav([1-9][0-9]?)?\\.com$",
		"(^|\\.)kpkuang\\.(bond|fun|info|one|us)$",
		"(^|\\.)ksyp0[1-9]\\.com$",
		"(^|\\.)lianren[1-7]\\.cc$",
		"(^|\\.)llyysp[0-9]{4}\\.top$",
		"(^|\\.)loibus(100|101|202|505|606)\\.top$",
		"(^|\\.)loibus\\.(cc|com|link)$",
		"(^|\\.)luchuxue([0-9]{0,5})\\.buzz$",
		"(^|\\.)maa18[0-2][0-9]\\.com$",
		"(^|\\.)madouqu[0-9]{2}\\.cc$",
		"(^|\\.)mdyy[0-1][0-9]\\.com$",
		"(^|\\.)mdyy[0-9][0-9]\\.cc$",
		"(^|\\.)mimi[0-9]{3}\\.com$",
		"(^|\\.)mimi\\.(cfd|lol|onl|sbs|so|vc)$",
		"(^|\\.)mitao[1-3]\\.tv$",
		"(^|\\.)mmtv[1-5]\\.life$",
		"(^|\\.)mmwz\\.(fun|in|la|me|one|pro|pw|run|tv|ws)$",
		"(^|\\.)momotk([1-9][0-9]?)?\\.uno$",
		"(^|\\.)nnbw[0-2]\\.xyz$",
		"(^|\\.)putaozy([1-9][0-9]?)?\\.com$",
		"(^|\\.)qqbs([0-9]{2})?\\.vip$",
		"(^|\\.)qqsp0[1-3]\\.com$",
		"(^|\\.)rbp[1-9][0-9]\\.com$",
		"(^|\\.)rbyh[1-9]\\.buzz$",
		"(^|\\.)rule34\\.(asia|us|world|xxx|xyz)$",
		"(^|\\.)sao[1-6]0000\\.com$",
		"(^|\\.)sbyjs2[6-9]\\.buzz$",
		"(^|\\.)sg11[3-9]\\.me$",
		"(^|\\.)shdd[0-9]{2}\\.buzz$",
		"(^|\\.)shuangmawei[0-9]{2}\\.buzz$",
		"(^|\\.)smgsp[0-2]?\\.buzz$",
		"(^|\\.)spankwire[1-9]\\.com$",
		"(^|\\.)ssphb[0-9]{2}\\.xyz$",
		"(^|\\.)supxxx[0-9]{0,2}\\.com$",
		"(^|\\.)sycf[0-9]?\\.xyz$",
		"(^|\\.)syjc1[1-6]\\.xyz$",
		"(^|\\.)tianbiav([1-9]|1[0-6])?\\.com$",
		"(^|\\.)tqav[1-9][0-9]\\.com$",
		"(^|\\.)tt[1-2][0-9]\\.tv$",
		"(^|\\.)ttghg[1-9][0-9]\\.xyz$",
		"(^|\\.)tttv([1-9][0-9]?|100)\\.com$",
		"(^|\\.)twav[1-9]\\.xyz$",
		"(^|\\.)twseb([1-9][0-9]?)?\\.com$",
		"(^|\\.)uu[a-z][1-9][0-9]?\\.com$",
		"(^|\\.)whtdh0[1-3]\\.cc$",
		"(^|\\.)wmm33[0-5]\\.com$",
		"(^|\\.)wyfuli[1-9]\\.net$",
		"(^|\\.)xaa[0-9]?\\.one$",
		"(^|\\.)xaxy[0-9]{2}\\.one$",
		"(^|\\.)xg[0-9]{2}\\.vip$",
		"(^|\\.)xgyy([0-9]{2})?\\.vip$",
		"(^|\\.)xingdh([0-9]{3})?\\.xyz$",
		"(^|\\.)xlywz[1-9]?\\.cc$",
		"(^|\\.)xomfsp[1-9]?\\.buzz$",
		"(^|\\.)xv[0-9]{4}\\.top$",
		"(^|\\.)xxxyaom[0-9]{2}\\.buzz$",
		"(^|\\.)yiren[1-9][0-9]\\.com$",
		"(^|\\.)youyuapi\\..+$",
		"(^|\\.)yrrj[1-9]?\\.buzz$",
		"(^|\\.)yygc[0-9]?\\.xyz$",
		"(^|\\.)zayy([0-9]{0,3})?\\.xyz$",
		"(^|\\.)zh9[0-7]\\.net$",
		"(^|\\.)zy[0-9]{4}\\.top$",
		"(^|\\.)zztt[1-6][0-9]\\.com$",
		"(^|\\.)zzz[1-9][0-9]\\.top$",
	}
	r := rand.New(rand.NewSource(7))
	labels := []string{"foo", "bar", "13mei5", "18jmtios12", "91porn", "91porn123", "jav777", "example", "luchuxue12345", "zayy123", "unknown"}
	tlds := []string{"com", "net", "org", "buzz", "xyz", "tv", "amazonaws.com"}
	for _, expr := range patterns {
		re, err := regexp.Compile(expr)
		if err != nil {
			t.Fatal(err)
		}
		fn := fastRegexpMatcher(expr)
		if fn == nil {
			t.Fatalf("not compiled: %s", expr)
		}
		cases := []string{"", "com", "foo.com", "sub.foo.com", "foo12.com", "x.foo12.com", "notfoo.com", "router.lan"}
		for i := 0; i < 300; i++ {
			a := labels[r.Intn(len(labels))]
			b := tlds[r.Intn(len(tlds))]
			cases = append(cases, a+"."+b, "sub."+a+"."+b)
		}
		for _, s := range cases {
			want := re.MatchString(s)
			got := fn(s)
			if want != got {
				t.Fatalf("%s on %q: want %v got %v", expr, s, want, got)
			}
		}
	}
}

func TestBuiltinMatcherEquivalentGeneratedNonBoundary(t *testing.T) {
	patterns := []string{
		`.+\.awsdns-[0-9][0-9]\.(co\.uk|com|net|org)$`,
		`.+\.dkr\.ecr\.[^\.]+\.amazonaws\.com$`,
		`^[0-9]+vod-adaptive\.akamaized\.net$`,
		`^chatgpt-async-webps-prod-\S+-\d+\.webpubsub\.azure\.com$`,
		`^epicgames-download\d\.akamaized\.net$`,
		`^ewcdn[0-9]+\.nowe\.com$`,
		`^github-production-release-asset-[0-9a-zA-Z]{6}\.s3\.amazonaws\.com$`,
		`^hses[1-7]?\.akamaized\.net$`,
		`javdb\d+\.com$`,
	}
	r := rand.New(rand.NewSource(11))
	labels := []string{"x", "12", "awsdns-12", "dkr.ecr.us", "javdb123", "chatgpt-async-webps-prod-a-2", "unknown"}
	suffixes := []string{".com", ".net", ".amazonaws.com", ".nowe.com", ".akamaized.net", ".webpubsub.azure.com"}
	for _, expr := range patterns {
		re, err := regexp.Compile(expr)
		if err != nil {
			t.Fatal(err)
		}
		fn := fastRegexpMatcher(expr)
		if fn == nil {
			t.Fatalf("not compiled: %s", expr)
		}
		cases := []string{"", "com", "x.awsdns-12.com", "x.dkr.ecr.us.amazonaws.com", "xjavdb123.com"}
		for i := 0; i < 300; i++ {
			cases = append(cases, labels[r.Intn(len(labels))]+suffixes[r.Intn(len(suffixes))])
		}
		for _, s := range cases {
			want, got := re.MatchString(s), fn(s)
			if want != got {
				t.Fatalf("%s on %q: want %v got %v", expr, s, want, got)
			}
		}
	}
}
