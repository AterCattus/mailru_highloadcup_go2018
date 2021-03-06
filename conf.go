package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"sort"
	"strconv"
	"sync"
)

const (
	hitsThresh     = 3
	browsersThresh = 3
)

type (
	IP      uint32
	IPRange struct {
		IP       IP
		IPMasked IP
		Mask     IP
	}

	InMy struct {
		Browsers [][]byte
		Hits     [][]byte
		Name     []byte
		Email    []byte
	}

	JsonPiper struct {
		strBrowsers []byte
		strCompany  []byte
		strCountry  []byte
		strEmail    []byte
		strHits     []byte
		strJob      []byte
		strName     []byte
		strPhone    []byte
	}
)

func (ip IP) String() string {
	return fmt.Sprintf(`%d.%d.%d.%d`, ip>>24, (ip>>16)&0xFF, (ip>>8)&0xFF, ip&0xFF)
}

func (r IPRange) Contains(ip IP) bool {
	return r.IPMasked == ip&r.Mask
}

func searchIPInNetworks(ip IP, networks []IPRange) bool {
	min, max := 0, len(networks)

	for min < max {
		medium := (min + max) / 2

		if networks[medium].Contains(ip) {
			return true
		} else if netIP := networks[medium].IP; ip < netIP {
			max = medium
		} else {
			min = medium + 1
		}
	}
	return false
}

func Fast(inRdr io.Reader, out io.Writer, networks []string) {
	type ResultRow struct {
		Line []byte
		Pos  int
	}
	var (
		userAgentRe = regexp.MustCompile(`Chrome/(60.0.3112.90|52.0.2743.116|57.0.2987.133)`)
		results     []ResultRow
		resultsMu   sync.Mutex
	)

	var netParsed = parseNetworksMy(networks)

	var wg sync.WaitGroup

	buf, _ := ioutil.ReadAll(inRdr)
	bufPos := 0

	var jp JsonPiper
	jp.init()

	userId := 1
	for bufPos < len(buf) {
		nPos := bytes.IndexByte(buf[bufPos:], '\n')
		if nPos == -1 {
			nPos = len(buf) - 1
		}

		line := buf[bufPos : bufPos+nPos]
		bufPos += nPos + 1

		wg.Add(1)
		go func(userId int, line []byte) {
			hitsCnt := 0
			browsersCnt := 0

			jsonPipe := jp.setupScanner(line)

			var in InMy
			for {
				if !jsonPipe(&in) {
					break
				}

			loop:
				for _, hit := range in.Hits {
					hitIP, _ := parseIP(string(hit))

					if searchIPInNetworks(hitIP, netParsed) {
						if hitsCnt++; hitsCnt >= hitsThresh {
							break loop
						}
					}
				}

				if hitsCnt < hitsThresh {
					continue
				}

				for _, browser := range in.Browsers {
					if !userAgentRe.Match(browser) {
					} else if browsersCnt++; browsersCnt >= browsersThresh {
						break
					}
				}

				if browsersCnt < browsersThresh {
					continue
				}

				email := bytes.Replace(in.Email, []byte(`@`), []byte(` [at] `), 1)

				var resultRow ResultRow
				resultRow.Line = []byte(fmt.Sprintf("[%d] %s <%s>\n", userId, in.Name, email))
				resultRow.Pos = userId

				resultsMu.Lock()
				results = append(results, resultRow)
				resultsMu.Unlock()
			}

			wg.Done()
		}(userId, line)

		userId++
	}

	wg.Wait()

	fmt.Fprintf(out, "Total: %d\n", len(results))

	sort.Slice(results, func(i, j int) bool {
		return results[i].Pos < results[j].Pos
	})

	for _, result := range results {
		out.Write(result.Line)
	}
}

func parseIP(s string) (ip IP, n int) {
	var oct uint32
	var ch byte
	shift := uint32(24)
	for n, ch = range []byte(s) {
		if ch == '.' {
			ip = ip + IP(oct<<shift)
			oct = 0
			shift -= 8
		} else if ch >= '0' && ch <= '9' {
			oct = (oct * 10) + uint32(ch-'0')
		} else {
			break
		}
	}

	if oct > 0 {
		ip = ip + IP(oct)
	}

	return
}

func parseNetworksMy(netRaw []string) (netParsed []IPRange) {
	netParsed = make([]IPRange, len(netRaw))

	var ipR IPRange

	for i, n := range netRaw {
		ip, l := parseIP(n)
		mask, _ := strconv.ParseUint(n[l+1:], 10, 32)

		ipR.IP = ip
		ipR.Mask = IP(0xFFFFFFFF << (32 - mask))
		ipR.IPMasked = ipR.IP & ipR.Mask

		netParsed[i] = ipR
	}

	sort.Slice(netParsed, func(i, j int) bool {
		return netParsed[i].IP < netParsed[j].IP
	})

	return
}

func (jp *JsonPiper) init() {
	jp.strBrowsers = []byte(`browsers`)
	jp.strCompany = []byte(`company`)
	jp.strCountry = []byte(`country`)
	jp.strEmail = []byte(`email`)
	jp.strHits = []byte(`hits`)
	jp.strJob = []byte(`job`)
	jp.strName = []byte(`name`)
	jp.strPhone = []byte(`phone`)
}

// {"browsers":["foo",..],"company":"Tavu","country":"Albania","email":"tHall@Fiveclub.edu","hits":["151.62.127.96",...],"job":"Staff Scientist","name":"Billy Stephens","phone":"508-76-84"}
func (jp *JsonPiper) setupScanner(js []byte) func(in *InMy) bool {
	var pos int

	checkCh := func(want byte) (c byte) {
		c = js[pos]
		pos++
		if c != want {
			panic(`checkCh. want:` + string(want) + ` got:` + string(c))
		}
		return
	}

	getCh := func() (c byte) {
		c = js[pos]
		pos++
		return
	}

	fetchString := func() []byte {
		checkCh('"')

		p := bytes.IndexByte(js[pos:], '"')
		s := js[pos : pos+p]
		pos += p + 1

		return s
	}

	fetchSliceOfStrings := func() (slice [][]byte) {
		checkCh('[')
		for {
			slice = append(slice, fetchString())
			c := getCh()
			if c == ']' {
				break
			} else if c == ',' {
			} else {
				panic(`fetchSliceOfStrings`)
			}
		}
		return
	}

	checkCh('{')

	return func(in *InMy) bool {
		for {
			if pos >= len(js) {
				return false
			}
			section := fetchString()
			checkCh(':')

			if bytes.Equal(section, jp.strBrowsers) {
				in.Browsers = fetchSliceOfStrings()
			} else if bytes.Equal(section, jp.strCompany) {
				fetchString()
			} else if bytes.Equal(section, jp.strCountry) {
				fetchString()
			} else if bytes.Equal(section, jp.strEmail) {
				in.Email = fetchString()
			} else if bytes.Equal(section, jp.strHits) {
				in.Hits = fetchSliceOfStrings()
			} else if bytes.Equal(section, jp.strJob) {
				fetchString()
			} else if bytes.Equal(section, jp.strName) {
				in.Name = fetchString()
			} else if bytes.Equal(section, jp.strPhone) {
				fetchString()
			} else {
				panic(`Unknown section: ` + string(section))
			}

			c := getCh()
			if c == ',' {
			} else if c == '}' {
				break
			} else {
				panic(`WTF end:` + string(c))
			}
		}

		return true
	}
}
