# 1 Query
Question: shown the name of employee who birth between 1967 and 1970, and have middle name as A, B or C.
Correct Result: 
| fname  | minit | Iname | bdate      |
|--------|-------|-------|------------|
| Kim    | c     | Grace | 1970-10-23 | 
| Naveen | B     | Drew  | 1970-05-23 |
| Jon    | c     | Jones | 1967-11-14 |
| Arnold | A     | Head  | 1967-05-19 |

___
Full Point Correct Answer:
___
  select	fname, minit, lname, bdate
  from	employee
  where	minit in ('A', 'B', 'C')
    and ((bdate between '1967-01-01' and '1967-12-31' )
    or  (bdate between '1970-01-01' and '1970-12-31'))
  order by 
    bdate desc;
___

# 2 Query
| dname  | dlocation | mgr_start_date |
|--------|--------------|------------|
| Kim    | Houston     | 1971-06-19 | 
___
Full Point Correct Answer:
___
 select
	d.dname,
	dl.dlocation,
	d.mgr_start_date
from 
	department as d
join dept_locations as dl on d.Dnumber = dl.Dnumber
where mgr_start_date < '1975-1-1'
___

## 3 Query Aggregration
| plocation | numberofproject |
|-|-|
| Houston     | 2 | 
| Stafford     | 2 | 
___
Full Point Correct Answer:
___
select plocation, COUNT(*) as "numberofproject"
from project
group by plocation
having count(*) > 1
___

## 4 Query aggregration

| answer |
|-|
| 3 | 
___
Full Point Correct Answer:
___
select 
	count(distinct e.fname)
from
	employee as e
join 
	dependent as dp on e.ssn = dp.essn
where 
	dp.relationship = 'Daughter' or
	dp.relationship = 'Son' 
___

## 5 Query aggregration
| dname | answer |
|-|-|
| Administration| 2 | 
| Hardware | 3 | 
| Research | 2 | 
| Sales | 9 | 
| Software | 3 | 

___
Full Point Correct Answer:
___
SELECT		D.dname, COUNT (DISTINCT E.super_ssn) AS answer
FROM		DEPARTMENT AS D, EMPLOYEE AS E
WHERE		D.dnumber = E.dno
	AND	E.super_ssn IS NOT NULL
GROUP BY	D.dname;
___

## 6 Query aggregration
| Project Name | Project Location | Dept Name | Dept Location |
| - | - | - | - |
| ProductX | Bellaire | Research | Bellaire |
| ProductY | Sugarland | Research | Sugarland |
| ProductZ | Houston | Research | Houston |
| Computerization | Stafford | Administration | Stafford |
| Reorganization | Houston | Headquarters | Houston |
| Newbenefits | Stafford | Administration | Stafford |
___
Full Point Correct Answer:
___
SELECT		PJ.pname AS "Project Name", PJ.plocation AS "Project Location",
		D.dname AS "Dept Name", L.dlocation AS "Dept Location"
FROM		PROJECT AS PJ, DEPARTMENT AS D, DEPT_LOCATIONS AS L
WHERE		L.dnumber = D.dnumber
	AND	D.dnumber = PJ.dnum
	AND	PJ.plocation = L.dlocation;
___

## 7 Query
| ssn | bdate | ssn | bdate |
| - | - | - | - |
| 987987987 | 1959-03-29 | 987654321 | 1931-06-20 |
| 987654321 | 1931-06-20 | 888665555 | 1927-11-10 |
___
Full Point Correct Answer:
___
SELECT  ssn, bdate, super_ssn,
	(
	SELECT		bdate
	FROM		EMPLOYEE AS E_in
	WHERE		E.super_ssn = E_in.ssn
	)
FROM		EMPLOYEE AS E
WHERE		E.super_ssn IS NOT NULL
	AND	ssn LIKE '__7______'
ORDER BY	ssn DESC
___

## 8 Query aggregration
| Dept Name | Quantity | Average |
| - | - | - |
| Hardware | 6 | 68750.00 |
| Software | 3 | 76000.00 |
___
Full Point Correct Answer:
___
select dname as "Dept Name", count(ssn) as Quantity, round(avg(salary),2) as Average
from employee, department
where sex = 'M' and dno = dnumber and (salary >= 50000 and salary <= 100000)
group by dname
having count(ssn) > 2
___

## 9 Query
| Dept Name | Quantity |
| - | - |
| OS | Software |
| DatabaseSystems | Software |
| Middleware | Software |
| Null | Sales |
___
Full Point Correct Answer:
___
select all p.pname,d.dname
from project p
full outer join department d on p.dnum=d.dnumber 
where d.dname like 'S%'
___

## 10 Query
| ssn | salary |
| - | - |
| 222222200 | 92000.00 |
| 111111100 | 85000.00 |
| 444444400 | 89000.00 |
| 666666600 | 96000.00 |
___
Full Point Correct Answer:
___
select e.ssn,e.salary
from employee e,department d
where e.dno=d.dnumber and e.salary>=85000
___

## 11 Insert 
หากต้องการเพิ่มข้อมูลการทำงานของแผนก Marketing หมายเลขแผนก 9 ผู้จัดการแผนกเป็น ‘222222222’ และเข้ารับตำแหน่งผู้จัดการแผนกเมื่อ 1997-01-15 จะต้องใช้คำสั่งอย่างไร สามารถเพิ่มข้อมูลได้หรือไม่ หากเพิ่มไม่ได้เพราะเหตุใด และมีวิธีแก้ไขอย่างไร
___
Did the sql command works:  No
command:
INSERT INTO department (dname, dnumber) VALUES ('Marketing', 9, '222222222', '1970-01-15')
Reason from workable command:   ละเมิด referential integrity constraint ไม่พบพนง.รหัส ‘222222222’ ในตาราง Employee
หากเพิ่มไม่ได้ ต้องแก้ไขอย่างไร: ต้องกำหนดผู้จัดการเป็นพนง.ที่มีในตาราง Employee
___

## 12 INSERT
ต้องการเพิ่มข้อมูลการทำงานของพนักงานรหัส ‘111111100’ โครงการเลขที่ 62 จำนวน 527 ชม. จะต้องใช้คำสั่งอย่างไร สามารถเพิ่มข้อมูลได้หรือไม่ หากเพิ่มไม่ได้เพราะเหตุใด และมีวิธีแก้ไขอย่างไร
___
เพิ่มได้หรือไม่ได้ : เพิ่มได้ (ที่ควบคุมโครงการ)
คำสั่งที่ใช้ในการเพิ่มข้อมูลคือ: INSERT INTO WORKS_ON VALUES (‘111111100’, 62, 527)
หากเพิ่มไม่ได้เนื่องจาก: -
หากเพิ่มไม่ได้ต้องแก้ไขอย่างไร: -
___

## 13 UPDATE
ต้องการปรับชั่วโมงการทำงานของพนักงานที่ทำงานในโครงการหมายเลข 62 เพิ่มชั่วโมงทำงานให้พนักงานที่มีชั่วโมงทำงานมากกว่า 50 ชั่วโมงเป็น 48 ชั่วโมง จะต้องใช้คำสั่งอย่างไร สามารถปรับปรุงข้อมูลได้หรือไม่ หากปรับปรุงไม่ได้เพราะเหตุใด และมีวิธีแก้ไขอย่างไร
___
ปรับได้หรือไม่ได้: ปรับได้
คำสั่งที่ใช้ในการปรับปรุงข้อมูลคือ: UPDATE WORKS_ON SET hours = 48 WHERE pno = 62 AND hours > 50
หากปรับได้จะมีรายการข้อมูลที่ถูกปรับกี่รายการ: 1
หากปรับไม่ได้เนื่องจาก: -
หากปรับไม่ได้ต้องแก้ไขอย่างไร: -
___

## 14 UPDATE
ต้องการปรับปรุงข้อมูลผู้ติดตาม (Dependent) ของพนักงานรหัส ‘444444400’ ที่มีชื่อ ‘Tommy’ ให้เปลี่ยนชื่อเป็น ‘Johnny’ จะต้องใช้คำสั่งอย่างไร สามารถปรับปรุงข้อมูลได้หรือไม่ หากปรับปรุงไม่ได้เพราะเหตุใด และมีวิธีแก้ไขอย่างไร
___
ปรับได้หรือไม่ได้: ปรับไม่ได้
คำสั่งที่ใช้ในการปรับปรุงข้อมูลคือ: UPDATE DEPENDENT SET dependent_name = 'Johnny' WHERE essn = '444444400' AND dependent_name = 'Tommy'
หากปรับได้จะมีรายการข้อมูลที่ถูกปรับกี่รายการ: 0
หากปรับไม่ได้เนื่องจาก: ละเมิด primary key constraint เนื่องจากพนักงานรหัส ‘444444400’ มีผู้ติดตามที่ชื่อ ‘Johnny’ อยู่แล้วในตาราง Dependent
หากปรับไม่ได้ต้องแก้ไขอย่างไร: ต้องกำหนดเป็นชื่ออื่นที่ไม่ใช่ ‘Johnny’ หรือลบผู้ติดตามคนเดิมที่ชื่อ ‘Johnny’ ออกก่อน
___

## 15 DELETE
ต้องการลบข้อมูลโครงการหมายเลข 61 จะต้องใช้คำสั่งอย่างไร สามารถลบข้อมูลได้หรือไม่ หากลบไม่ได้เพราะเหตุใด และมีวิธีแก้ไขอย่างไร
___
ลบได้หรือไม่ได้: ลบไม่ได้
คำสั่งที่ใช้ในการลบคือ: DELETE FROM PROJECT WHERE pnumber = 61
หากลบได้จะมีรายการข้อมูลที่ถูกลบกี่รายการ: -
หากลบไม่ได้เนื่องจาก: ละเมิด referential integrity constraint (foreign key constraint) เนื่องจากโครงการหมายเลข 61 ถูกอ้างอิงอยู่จากตาราง WORKS_ON
หากลบไม่ได้ต้องแก้ไขอย่างไร: สามารถแก้ไขได้h 2 วิธี:
- วิธีที่ 1: ลบข้อมูลการทำงานในโครงการหมายเลข 61 ในตาราง WORKS_ON ออกก่อน แล้วค่อยกลับมาลบโครงการในตาราง PROJECT
- วิธีที่ 2: เปลี่ยนโครงการหมายเลข 61 ในตาราง WORKS_ON เป็นโครงการอื่นก่อน
___
