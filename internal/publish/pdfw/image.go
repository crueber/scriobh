package pdfw

import (
	"fmt"
)

// Image is a raster image XObject.
type Image struct {
	Res  string // Im1, Im2, ...
	idx  int
	obj  int
	dict string
	data []byte
	// compress asks for the data to be Flate-compressed here; when false the
	// data is already in the filter the dictionary names.
	compress bool
	smask    *Image
}

// ImageSpec describes an image's pixels.
type ImageSpec struct {
	Width, Height int
	// ColorSpace is a PDF colour space: /DeviceGray, /DeviceRGB, or an
	// [/Indexed /DeviceRGB n <hex>] array.
	ColorSpace string
	BPC        int
	// Filter is the filter the data is already in ("/DCTDecode",
	// "/FlateDecode"), or "" for raw samples that are compressed here.
	Filter string
	// Parms is the DecodeParms dictionary for a Flate stream with a PNG predictor.
	Parms string
	// Mask is a soft mask (an 8-bit grey image of the same size) or nil.
	Mask *Image
}

// NewImage registers an image. The data is written as it is.
func (d *Doc) NewImage(spec ImageSpec, data []byte) *Image {
	im := &Image{Res: fmt.Sprintf("Im%d", len(d.images)+1), idx: len(d.images), data: data, smask: spec.Mask}
	dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent %d", spec.Width, spec.Height, spec.ColorSpace, spec.BPC)
	switch spec.Filter {
	case "":
		im.compress = true
	default:
		dict += " /Filter " + spec.Filter
		if spec.Parms != "" {
			dict += " /DecodeParms " + spec.Parms
		}
	}
	im.dict = dict
	d.images = append(d.images, im)
	return im
}

func (im *Image) write(d *Doc) {
	im.obj = d.Alloc()
	dict := im.dict
	if im.smask != nil {
		dict += fmt.Sprintf(" /SMask %d 0 R", im.smask.obj)
	}
	if im.compress {
		d.PutStream(im.obj, dict, im.data, true)
	} else {
		d.PutRawStream(im.obj, dict, im.data)
	}
}

// NewICC adds an ICC profile to the document as the colour space of the images
// that name it, and returns the colour space ("[/ICCBased n 0 R]") to put in an
// ImageSpec. The same profile is written once. The caller has checked that it is
// a profile of RGB data.
func (d *Doc) NewICC(profile []byte) string {
	key := string(profile)
	if cs, ok := d.icc[key]; ok {
		return cs
	}
	n := d.Alloc()
	d.PutStream(n, "/N 3 /Alternate /DeviceRGB", profile, true)
	cs := fmt.Sprintf("[/ICCBased %d 0 R]", n)
	if d.icc == nil {
		d.icc = map[string]string{}
	}
	d.icc[key] = cs
	return cs
}
