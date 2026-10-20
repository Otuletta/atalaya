#!/bin/sh
# <xbar.title>Atalaya</xbar.title>
# <xbar.desc>El estado de tus proyectos en la barra de menús.</xbar.desc>
# <xbar.author>Oscar Tuletta</xbar.author>
# <xbar.dependencies>atalaya</xbar.dependencies>
#
# Copia este archivo a tu carpeta de plugins de SwiftBar (https://swiftbar.app).
# El ".30s" del nombre es el intervalo. Lee el estado que mantiene un
# `atalaya` abierto (o `atalaya serve`), así que es instantáneo.
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/go/bin:$PATH"
exec atalaya prompt --format swiftbar
