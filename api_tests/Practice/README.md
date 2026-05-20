# Practice API Tests

Student requests (seq 1-5): use a STUDENT JWT in {{token}}.
Lecturer requests (seq 1-5 in Lecturer/ subfolder): use an ADMIN or LECTURER JWT in {{token}}.
The {{baseUrl}} variable should be set to http://localhost:8080/api/v1 in your Bruno environment.

Optional 423 test: activate an exam via PUT {{baseUrl}}/lecturer/exams/:id/activate, then run
Student/Submit.bru -- expect HTTP 423 response.

Note: These tests are not run in CI. They are intended for manual pre-deploy QA.
