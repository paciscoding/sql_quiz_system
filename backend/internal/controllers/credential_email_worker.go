package controllers

import (
	"log"

	"github.com/paciscoding/sql-quiz-system/internal/services"
)

// credentialRecipient is one credential to deliver, reduced to just the three
// fields a send needs. Both bulk credential paths -- the rotating roster import
// (LecturerController.ImportStudentsWithCredentials) and the opt-in email on
// bulk password reset (UserController.BulkResetPassword) -- map their own row
// type onto this, so the delivery rules below are written once and cannot
// drift between the two.
type credentialRecipient struct {
	// Email is the destination address; a recipient only reaches this type
	// once the caller has confirmed it is non-empty.
	Email string
	// LoginID is what the student types to log in, and is also the name a
	// failure is reported under in the job registry.
	LoginID string
	// Password is plaintext, in memory only, for the duration of the send.
	Password string
}

// startCredentialEmailJob registers a progress job for `recipients` and sends
// them from ONE background goroutine, serially. Serial by design: a single
// SMTP conversation at a time keeps a large batch under Gmail's rate limits
// instead of bursting into a rejection.
//
// It returns the job id to hand back to the client, or "" when nothing was
// started (no recipients, or a missing dependency). A "" id is also possible
// with sends genuinely in flight -- see the crypto/rand degradation below --
// which is why the caller must not read "" as "nothing was sent".
//
// The goroutine is deliberately detached from the request: it takes no
// context, so the admin's browser disconnecting cannot cancel a send that is
// already partly done.
func startCredentialEmailJob(auth *services.AuthService, jobs *services.EmailJobRegistry, adminID int, recipients []credentialRecipient) string {
	if len(recipients) == 0 || auth == nil || jobs == nil {
		return ""
	}

	job := jobs.Register(adminID, len(recipients))
	if job.ID == "" {
		// crypto/rand failed inside Register, which degrades to an unregistered
		// job rather than a predictable id (see EmailJobRegistry.Register).
		// That produces the same "" the no-recipients case returns, so say so
		// explicitly here: the emails below DO still go out, just untracked --
		// every MarkSent/MarkFailed/Finish against an unregistered id is a no-op.
		log.Printf("credential-email: job registration failed (crypto/rand); sending %d email(s) without progress tracking", len(recipients))
	}

	go func(jobID string, batch []credentialRecipient) {
		defer jobs.Finish(jobID)
		for _, r := range batch {
			sendOneCredential(auth, jobs, jobID, r)
		}
	}(job.ID, recipients)

	return job.ID
}

// sendOneCredential sends a single credential and records the outcome in the
// job registry. It carries its OWN per-iteration recover(): the worker loop
// used to wrap a single recover() around the WHOLE send loop, so a panic on
// (say) row 3 of 200 abandoned rows 4-200 with no MarkFailed call for any of
// them -- Finish still ran, so the admin saw a job reporting "done" with a
// misleadingly small failure count while 197 rows silently got nothing.
// Recovering per-iteration means one bad row is recorded as failed and the
// loop moves on, exactly like a returned error from SendCredentialEmail.
//
// This also keeps a panic off the process's own crash path: the goroutine
// above is detached, and gin's Recovery middleware only covers the request
// goroutine, so an unrecovered panic here would take the whole server down.
func sendOneCredential(auth *services.AuthService, jobs *services.EmailJobRegistry, jobID string, r credentialRecipient) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("credential-email: worker panic for %q: %v", r.LoginID, rec)
			jobs.MarkFailed(jobID, r.LoginID)
		}
	}()
	if err := auth.SendCredentialEmail(r.Email, r.LoginID, r.Password); err != nil {
		log.Printf("credential-email: SendCredential failed for %q: %v", r.LoginID, err)
		jobs.MarkFailed(jobID, r.LoginID)
		return
	}
	jobs.MarkSent(jobID)
}
