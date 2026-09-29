package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
)

func main() {
	count := flag.Int("count", 12000, "sentence count")
	startID := flag.Uint64("start-id", 1, "first UUID suffix for non-overlapping batches")
	seed := flag.Int64("seed", 20260918, "random seed")
	flag.Parse()
	if *count < 1 || *startID < 1 || *startID >= 1<<48 || uint64(*count) > (1<<48)-*startID {
		fmt.Fprintln(os.Stderr, "count and start-id must fit positive 48-bit UUID suffixes")
		os.Exit(2)
	}
	rng := rand.New(rand.NewSource(*seed))
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	fmt.Fprint(w, `{"categories":[{"code":"short","name":"短句","sort_order":0},{"code":"long","name":"长句","sort_order":1},{"code":"rare","name":"少量分类","sort_order":2}],"sentences":[`)
	for i := 0; i < *count; i++ {
		if i > 0 {
			w.WriteByte(',')
		}
		category := "short"
		content := fmt.Sprintf("合成句子 %d：今天继续写代码 😀", i)
		switch {
		case i == *count-1:
			category = "long"
			content = "超长内容：" + strings.Repeat("界", 1001)
		case i%29 == 0:
			category = "rare"
			content = "少量分类：" + strings.Repeat("界", 3+rng.Intn(12))
		case i%7 == 0:
			category = "long"
			content = "较长内容：" + strings.Repeat("测试，", 20+rng.Intn(40))
		}
		uuid := fmt.Sprintf("00000000-0000-4000-8000-%012x", *startID+uint64(i))
		fmt.Fprintf(w, `{"uuid":%s,"category":%s,"content":%s,"source":"项目合成性能夹具","author":null}`, strconv.Quote(uuid), strconv.Quote(category), strconv.Quote(content))
	}
	fmt.Fprint(w, `]}`)
}
