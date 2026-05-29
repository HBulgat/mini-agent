// Package webapi hosts the gin HTTP layer that exposes the agent over
// REST + SSE. It implements uio.Sink (SSE push) and uio.Prompter
// (SSE-pushed approval cards + REST callback).
//
// Status: skeleton only. Iter-5 / Iter-6, gated on R10 / R11.
package webapi
