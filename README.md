##img2ansi

Renders raster images for a terminal using ANSI color codes.  Supported image
types are JPEG, PNG, and GIF (which may be animated).

	img2ansi motd.png
	img2ansi -animate -repeat=5 -scale https://i.imgur.com/872FDBm.gif
	img2ansi -h

Image converter based on @saikobee's nifty [tool](https://github.com/saikobee/bin/blob/master/img2ansi)

##Install

    go get github.com/bmatsuo/img2ansi

**NOTE:** Windows is not supported.

###Documentation

On [godoc.org](http://godoc.org/github.com/bmatsuo/img2ansi)

####Saving images

The output of `img2ansi` can be redirected to a file and replayed later using
`cat`.

    img2ansi https://i.imgur.com/872FDBm.gif > awesome
    cat awesome

Better yet, the output can be compressed using a program like `gzip`

    img2ansi https://i.imgur.com/872FDBm.gif | gzip > awesome.gz
    gzip -dc awesome.gz

###Manipulating images

For simple manipulation and combination of images and text unix-friendly tools
like those provided by [ImageMagick](http://www.imagemagick.org/) can be piped
directly into `img2ansi.

    convert -background transparent -fill red -pointsize 72 label:"blorp" gif:- | img2ansi -scale
