package utils

import (
"sync"

"github.com/liuzl/gocc"
)

var (
    t2sOnce sync.Once
    t2s     *gocc.OpenCC
)

func getT2S() *gocc.OpenCC {
    t2sOnce.Do(func() {
        var err error

        t2s, err = gocc.New("t2s")
        if err != nil {
            panic(err)
        }
    })

    return t2s
}

// IsNeedTraditionalToSimple 判断字符串是否包含繁体字
func IsNeedTraditionalToSimple(s string) (bool,string) {
    if s == "" {
        return false,""
    }
    simple := ToSimpleChinese(s)
    // 如果转换后的结果和原字符串不同，说明有繁体字
    return simple!= s,simple
}

// ToSimpleChinese 繁体转简体
func ToSimpleChinese(s string) string {
    if s == "" {
        return s
    }

    out, err := getT2S().Convert(s)
    if err != nil {
        return s
    }

    return out
}

