const fs = require('fs');

async function test() {
  const FormData = require('form-data');
  const axios = require('axios');
  
  const form = new FormData();
  form.append('path', '/DATA');
  form.append('relativePath', 'myfolder/AppData/test.txt');
  form.append('chunkNumber', '1');
  form.append('totalChunks', '1');
  form.append('identifier', '12345');
  form.append('filename', 'test.txt');
  form.append('file', Buffer.from('hello'), { filename: 'test.txt' });

  try {
    const res = await axios.post('http://localhost:8080/v2/nimoos/file/upload', form, {
      headers: form.getHeaders()
    });
    console.log(res.status, res.data);
  } catch (err) {
    console.log(err.response.status, err.response.data);
  }
}
test();
