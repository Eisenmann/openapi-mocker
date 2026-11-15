document.querySelectorAll("table.sortable").forEach(function(t){
  t.querySelectorAll("th").forEach(function(th,i){
    th.addEventListener("click",function(){
      var body=t.tBodies[0],rows=Array.prototype.slice.call(body.rows),dir=th.dataset.dir==="asc"?-1:1;
      th.dataset.dir=dir===1?"asc":"desc";
      rows.sort(function(a,b){
        var x=a.cells[i].dataset.v!==undefined?a.cells[i].dataset.v:a.cells[i].textContent,
            y=b.cells[i].dataset.v!==undefined?b.cells[i].dataset.v:b.cells[i].textContent,
            nx=parseFloat(x),ny=parseFloat(y);
        return (isNaN(nx)||isNaN(ny)?x.localeCompare(y):nx-ny)*dir;
      });
      rows.forEach(function(r){body.appendChild(r)});
    });
  });
});
