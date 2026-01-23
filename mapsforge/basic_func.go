package mapsforge

import (
	"fmt"
	"math"
	"strconv"
)

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (a LatLon) less(b LatLon) bool {
	if a.lat != b.lat {
		return a.lat < b.lat
	}
	return a.lon < b.lon
}

func (a LatLon) eq(b LatLon) bool {
	return !a.less(b) && !b.less(a)
}

func (pos *LatLon) ToXY(zoom uint8) (int, int) {
	lat, lon := float64(pos.lat)/1e6, float64(pos.lon)/1e6
	n := math.Pow(2, float64(zoom))

	x := int((lon + 180.0) / 360.0 * n)
	y := int((1.0 - math.Log(math.Tan(lat/180*math.Pi)+(1/math.Cos(lat/180*math.Pi)))/math.Pi) / 2.0 * n)
	return x, y
}

func zic_eq(zi1, zi2 []ZoomIntervalConfig) bool {
	if len(zi1) != len(zi2) {
		return false
	}
	for i := 0; i < len(zi1); i++ {
		if zi1[i].base_zoom_level != zi2[i].base_zoom_level {
			return false
		}
		if zi1[i].min_zoom_level != zi2[i].min_zoom_level {
			return false
		}
		if zi1[i].max_zoom_level != zi2[i].max_zoom_level {
			return false
		}
	}
	return true
}

func (a *POIData) less(b *POIData) bool {
	if a.LatLon != b.LatLon {
		return a.LatLon.less(b.LatLon)
	}
	if a.layer != b.layer {
		return a.layer < b.layer
	}
	if len(a.tag_id) != len(b.tag_id) {
		return len(a.tag_id) < len(b.tag_id)
	}
	for i := range a.tag_id {
		if a.tag_id[i] != b.tag_id[i] {
			return a.tag_id[i] < b.tag_id[i]
		}
	}
	if a.has_name != b.has_name {
		return b.has_name
	}
	if a.has_name {
		return a.name < b.name
	}
	if a.has_house_number != b.has_house_number {
		return b.has_house_number
	}
	if a.has_house_number {
		return a.house_number < b.house_number
	}
	if a.has_elevation != b.has_elevation {
		return b.has_elevation
	}
	if a.has_elevation && a.elevation != b.elevation {
		return a.elevation < b.elevation
	}
	return false
}

func (a *POIData) eq(b *POIData) bool {
	return !a.less(b) && b.less(a)
}

func (p *POIData) ToString(stat *TagsStat) string {
	r := ""
	r += fmt.Sprintf("%s,layer=%d", p.LatLon.ToString(), p.layer)
	for _, tag := range p.tag_id {
		r += fmt.Sprintf(",%d(%s)", tag, stat.stat[tag].str)
	}
	if p.has_name {
		r += ",name=" + fmt.Sprintf("%#v", p.name)
	}
	if p.has_house_number {
		r += ",house_number=" + p.house_number
	}
	if p.has_elevation {
		r += ",ele=" + strconv.Itoa(int(p.elevation))
	}
	return r
}

func (w *WayProperties) ToString(stat *TagsStat) string {
	r := ""
	r += fmt.Sprintf("layer=%d", w.layer)
	for _, tag := range w.tag_id {
		r += fmt.Sprintf(",%d(%s)", tag, stat.stat[tag].str)
	}
	if w.has_name {
		r += ",name=" + fmt.Sprintf("%#v", w.name)
	}
	if w.has_house_number {
		r += ",house_number=" + w.house_number
	}
	if w.has_reference {
		r += ",ref=" + w.reference
	}
	if w.has_label_position {
		r += ",label_position=" + w.label_position.ToString()
	}

	for _, b := range w.block {
		r += "["
		for wi, ww := range b.data {
			if wi != 0 {
				r += ","
			}
			for _, node := range ww {
				r += node.ToString()
			}
		}
		r += "]"
	}
	return r
}

func (a *WayProperties) less(b *WayProperties) bool {
	if a.layer != b.layer {
		// XXX
		return a.layer < b.layer
	}
	if len(a.tag_id) != len(b.tag_id) {
		return len(a.tag_id) < len(b.tag_id)
	}
	for i := range a.tag_id {
		if a.tag_id[i] != b.tag_id[i] {
			return a.tag_id[i] < b.tag_id[i]
		}
	}

	if a.name != b.name {
		return a.name < b.name
	}
	if a.house_number != b.house_number {
		return a.house_number < b.house_number
	}
	if a.reference != b.reference {
		return a.reference < b.reference
	}
	if a.has_label_position != b.has_label_position {
		return b.has_label_position
	}
	if a.has_label_position && !a.label_position.eq(b.label_position) {
		return a.label_position.less(b.label_position)
	}

	if a.num_way_block != b.num_way_block {
		return a.num_way_block < b.num_way_block
	}
	for bi := 0; bi < len(a.block); bi++ {
		ad := a.block[bi].data
		bd := b.block[bi].data

		if len(ad) != len(bd) {
			return len(ad) < len(bd)
		}
		for wi := 0; wi < len(ad); wi++ {
			an := ad[wi]
			bn := bd[wi]
			if len(an) != len(bn) {
				return len(an) < len(bn)
			}
			for ni := 0; ni < len(an); ni++ {
				if !an[ni].eq(bn[ni]) {
					return an[ni].less(bn[ni])
				}
			}
		}
	}

	return false
}

type CmpByPOIData []POIData

func (a CmpByPOIData) Len() int      { return len(a) }
func (a CmpByPOIData) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a CmpByPOIData) Less(i, j int) bool {
	return a[i].less(&a[j])
}

type CmpByWayData []WayProperties

func (a CmpByWayData) Len() int      { return len(a) }
func (a CmpByWayData) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a CmpByWayData) Less(i, j int) bool {
	return a[i].less(&a[j])
}

type Uint32Slice []uint32

func (a Uint32Slice) Len() int           { return len(a) }
func (a Uint32Slice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a Uint32Slice) Less(i, j int) bool { return a[i] < a[j] }
