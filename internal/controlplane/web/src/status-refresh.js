export async function refreshAfterSubmit(refresh, setStatusError) {
  try {
    await refresh();
  } catch (requestError) {
    setStatusError(requestError.message);
    throw requestError;
  }
}
