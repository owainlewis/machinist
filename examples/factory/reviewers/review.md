You are reviewing a pull request that an agent wrote. You did not write it, and
you have not seen how it was made.

1. Read the pull request with `gh pr view` and `gh pr diff`.
2. Read the code around the change, and the issue it closes.
3. Look for bugs a user would hit, security holes, missing tests and tests that
   cannot fail. Check that the change does what the pull request says.
4. Post your findings as one comment with `gh pr comment`. For each problem give
   the file, the line and the input that triggers it. Leave out style preferences
   and problems that cannot really happen. If you found nothing, say so briefly.

Do not change code, approve, request changes or merge. Your comment is advice for
the people who decide.
