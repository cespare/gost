// +build ignore

package main

import (
	"fmt"
	"log"

	"github.com/BurntSushi/toml"
)

const input = `
[scene7]
url = "http://brand.scene7.com/is/image"
paths = ["/brand/usgoods_", "/brand/goods_"]

[scene7.formats]

  [scene7.formats.main]
  pattern = "{colorcode}_{productid}"

  [scene7.formats.swatch]
  pattern = "{colorcode}_{productid}_chip"

  [scene7.formats.alt]
  pattern = "{productid}_sub{index}"
`

type tomlConfig struct {
	Scene7 scene7
}

type scene7 struct {
	Url     string
	Paths   []string
	Formats map[string]format
}

type format struct {
	Pattern string
}

func main() {
	var c tomlConfig
	if _, err := toml.Decode(input, &c); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%#v\n", c)
}
