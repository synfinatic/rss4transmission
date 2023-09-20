package rest

/*
 * RSS4Transmission
 * Copyright (c) 2023 Aaron Turner  <aturner at synfin dot net>
 *
 * This program is free software: you can redistribute it
 * and/or modify it under the terms of the GNU General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or with the authors permission any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

type RestServer struct {
	e      *echo.Echo
	status string
}

func NewRestServer() *RestServer {
	r := &RestServer{
		e:      echo.New(),
		status: "init",
	}

	r.e.GET("/status", r.status)

	return r
}

func (r *RestServer) Start(uint16 port) {
	r.e.Logger.Fatal(r.e.Start(fmt.Sprintf(":%d", port)))
}

func (r *RestServer) status(c echo.Context) error {
	return c.String(http.StatusOK, r.status)
}
